package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chzyer/readline"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/lsp"
	"github.com/yusiwen/tinycode/session"
	"github.com/yusiwen/tinycode/skill"
	"github.com/yusiwen/tinycode/tool"
)

func init() {
	// .env 加载优先级（高 → 低）：
	//   1. 进程已有的环境变量（export OPENAI_API_KEY=***）
	//   2. ./.tinycode/.env（当前工作目录下的项目配置）
	//   3. ~/.tinycode/.env（用户 home 下的全局配置）
	godotenv.Load(filepath.Join(".tinycode", ".env"))
	godotenv.Load(filepath.Join(os.Getenv("HOME"), ".tinycode", ".env"))
}

func modePrompt(mode string) string {
	return fmt.Sprintf("[%s]> ", mode)
}

func main() {
	var apiKey string
	var baseURL string
	var model string
	var sessionDir string

	rootCmd := &cobra.Command{
		Use:   "tinycode",
		Short: "TinyCode - AI coding agent in Go",
		Long: `TinyCode is an AI-powered coding assistant built in Go.
It uses a ReAct loop to understand your requests and use tools (shell, filesystem) to accomplish them.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config file (defaults → ~/.tinycode/config.json → ./.tinycode/config.json)
			cfg := config.LoadConfig()

			// Expand $HOME in sessionDir
			if sessionDir != "" {
				cfg.SessionDir = os.ExpandEnv(sessionDir)
			} else {
				cfg.SessionDir = os.ExpandEnv(cfg.SessionDir)
			}

			// Resolve provider settings: CLI flag → env var → config file → hardcoded default
			if apiKey == "" {
				apiKey = os.Getenv("OPENAI_API_KEY")
			}
			if baseURL == "" {
				baseURL = os.Getenv("OPENAI_BASE_URL")
				if baseURL == "" {
					baseURL = cfg.Provider.BaseURL
					if baseURL == "" {
						baseURL = "https://api.deepseek.com"
					}
				}
			}
			if model == "" {
				model = os.Getenv("OPENAI_MODEL")
				if model == "" {
					model = cfg.Provider.Model
					if model == "" {
						model = "deepseek-v4-flash"
					}
				}
			}

			if apiKey == "" {
				return fmt.Errorf("API key not set; use --api-key or OPENAI_API_KEY env")
			}

			// Create provider
			provider := agent.NewDeepSeekProvider(apiKey, baseURL, model)

			// Initialize LSP if enabled
			if cfg.LSP.Enabled {
				lsp.Init(cfg.SessionDir)
			}

			// Create agent registry (registers plan, build, explore agents)
			reg := agent.NewRegistry()
			_ = reg // used indirectly via current mode

			// Apply agent overrides from config file
			for name, override := range cfg.Agents {
				if aCfg, err := reg.Get(name); err == nil {
					if override.MaxSteps > 0 {
						aCfg.MaxSteps = override.MaxSteps
					}
					if override.SystemPrompt != "" {
						aCfg.SystemPrompt = override.SystemPrompt
					}
					if override.AllowedTools != nil {
						aCfg.AllowedTools = override.AllowedTools
					}
					if override.DeniedTools != nil {
						aCfg.DeniedTools = override.DeniedTools
					}
				}
			}

			// Set default mode from config
			if cfg.DefaultMode != "" {
				reg.Set(cfg.DefaultMode)
			}

			// Create agent (starts with configured default mode)
			ag := agent.New(provider)
			ag.Config = reg.Current()
			ag.ShowThinking = true
			if cfg.ShowThinking != nil {
				ag.ShowThinking = *cfg.ShowThinking
			}
			if cfg.Verbose != nil {
				ag.Verbose = *cfg.Verbose
			}

			// Register tools (all tools, filtered at runtime by agent config)
			ag.AddTool(agent.Tool{
				Name:        tool.Bash().Name,
				Description: tool.Bash().Description,
				Parameters:  tool.Bash().Parameters,
				Execute:     tool.Bash().Execute,
			})
			ag.AddTool(agent.Tool{
				Name:        tool.ReadFile().Name,
				Description: tool.ReadFile().Description,
				Parameters:  tool.ReadFile().Parameters,
				Execute:     tool.ReadFile().Execute,
			})
			ag.AddTool(agent.Tool{
				Name:        tool.WriteFile().Name,
				Description: tool.WriteFile().Description,
				Parameters:  tool.WriteFile().Parameters,
				Execute:     tool.WriteFile().Execute,
			})
			ag.AddTool(agent.Tool{
				Name:        tool.SearchFiles().Name,
				Description: tool.SearchFiles().Description,
				Parameters:  tool.SearchFiles().Parameters,
				Execute:     tool.SearchFiles().Execute,
			})
			ag.AddTool(skill.NewCodeReviewSkill().ToTool())
			ag.AddTool(skill.NewGitCommitSkill().ToTool())
			ag.AddTool(lsp.ToolFactory(lsp.ToolGoToDefinition))
			ag.AddTool(lsp.ToolFactory(lsp.ToolFindReferences))
			ag.AddTool(lsp.ToolFactory(lsp.ToolHover))
			ag.AddTool(lsp.ToolFactory(lsp.ToolDocumentSymbols))
			ag.AddTool(tool.SandboxAllowTool())

			// Set security sandbox: restrict file access to project root
			tool.DefaultSandbox.ProjectRoot = "/home/yusiwen/git/ai/TinyCode"

			// Session
			store := session.NewStore(cfg.SessionDir)
			sess := store.Create("default")
			ag.SessionStore = sess

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Handle Ctrl+C
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			gracefulDone := make(chan struct{})
			go func() {
				// First Ctrl+C: graceful shutdown
				<-sigCh
				fmt.Println("\n\nSaving session...")
				sess.Flush()
				cancel()
				close(gracefulDone)

				// Second Ctrl+C within 2s: force exit
				select {
				case <-sigCh:
					fmt.Println("\nForce exit.")
					os.Exit(0)
				case <-time.After(2 * time.Second):
				}
			}()

			prompt := ""
			if len(args) > 0 {
				prompt = args[0]
			}

			if prompt != "" {
				// One-shot mode
				fmt.Printf("🤖 TinyCode (model: %s)\n", provider.Name())
				result, err := ag.Run(ctx, prompt)
				if err != nil {
					return fmt.Errorf("agent error: %w", err)
				}
				printMarkdown(result, cfg.GlamourStyle)
				return nil
			}

			// Interactive REPL mode
			modeName := reg.CurrentName()
			fmt.Printf("🤖 TinyCode (model: %s) — %s mode\n", provider.Name(), modeName)
			fmt.Println("Type your request, or /exit to quit. Press Tab to toggle plan/build mode.")

			// Readline config with Tab completer for mode switching
			rl, err := readline.New(modePrompt(modeName))
			if err != nil {
				return err
			}
			defer rl.Close()

			// Tab completion for commands
			rl.Config.AutoComplete = readline.NewPrefixCompleter(
				readline.PcItem("/exit"),
				readline.PcItem("/quit"),
				readline.PcItem("/verbose"),
				readline.PcItem("/thinking"),
				readline.PcItem("/plan"),
				readline.PcItem("/build"),
				readline.PcItem("/mode"),
			)

		replLoop:
			for {
				select {
				case <-gracefulDone:
					break replLoop
				default:
				}

				line, err := rl.Readline()
				if err != nil {
					break replLoop
				}

				// Switch mode when input is empty (Tab was pressed)
				if line == "" {
					reg.Switch()
					newMode := reg.CurrentName()
					ag.Config = reg.Current()
					rl.SetPrompt(modePrompt(newMode))
					fmt.Printf("Switched to %s mode\n", newMode)
					rl.Refresh()
					continue
				}

				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}

				// Handle commands
				if strings.HasPrefix(line, "/") {
					switch line {
					case "/exit", "/quit":
						break replLoop
					case "/verbose":
						ag.Verbose = !ag.Verbose
						status := "off"
						if ag.Verbose {
							status = "on"
						}
						fmt.Printf("Verbose mode %s\n", status)
						continue
					case "/thinking":
						ag.ShowThinking = !ag.ShowThinking
						status := "off"
						if ag.ShowThinking {
							status = "on"
						}
						fmt.Printf("Thinking display %s\n", status)
						continue
					case "/plan":
						if err := reg.Set("plan"); err != nil {
							fmt.Printf("⚠️  Error: %v\n", err)
							continue
						}
						ag.Config = reg.Current()
						rl.SetPrompt(modePrompt("plan"))
						fmt.Println("Switched to plan mode")
						rl.Refresh()
						continue
					case "/build":
						if err := reg.Set("build"); err != nil {
							fmt.Printf("⚠️  Error: %v\n", err)
							continue
						}
						ag.Config = reg.Current()
						rl.SetPrompt(modePrompt("build"))
						fmt.Println("Switched to build mode")
						rl.Refresh()
						continue
					case "/mode":
						fmt.Printf("Current mode: %s\n", reg.CurrentName())
						continue
					default:
						fmt.Printf("Unknown command: %s\n", line)
						continue
					}
				}

				result, err := ag.Run(ctx, line)
				if err != nil {
					fmt.Printf("⚠️  Error: %v\n", err)
					continue
				}
				printMarkdown(result, cfg.GlamourStyle)
				fmt.Println()
			}
			fmt.Println("\nBye!")
			return nil
		},
	}

	rootCmd.Flags().StringVar(&apiKey, "api-key", "", "API key (default: OPENAI_API_KEY env)")
	rootCmd.Flags().StringVar(&baseURL, "base-url", "", "API base URL (default: OPENAI_BASE_URL env, fallback https://api.deepseek.com)")
	rootCmd.Flags().StringVar(&model, "model", "", "Model name (default: OPENAI_MODEL env, fallback deepseek-v4-flash)")
	rootCmd.Flags().StringVar(&sessionDir, "session-dir", "", "Session storage directory (default: ~/.tinycode/sessions)")

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
