package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/yusiwen/tinycode/agent"
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
			// Expand $HOME in sessionDir
			if sessionDir == "" {
				sessionDir = os.ExpandEnv("$HOME/.tinycode/sessions")
			} else {
				sessionDir = os.ExpandEnv(sessionDir)
			}

			// Resolve config
			if apiKey == "" {
				apiKey = os.Getenv("OPENAI_API_KEY")
			}
			if baseURL == "" {
				baseURL = os.Getenv("OPENAI_BASE_URL")
				if baseURL == "" {
					baseURL = "https://api.deepseek.com"
				}
			}
			if model == "" {
				model = os.Getenv("OPENAI_MODEL")
				if model == "" {
					model = "deepseek-v4-flash"
				}
			}

			if apiKey == "" {
				return fmt.Errorf("API key not set; use --api-key or OPENAI_API_KEY env")
			}

			// Create provider
			provider := agent.NewDeepSeekProvider(apiKey, baseURL, model)

			// Create agent
			ag := agent.New(provider)
			ag.MaxSteps = 20

			// Register tools
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
			store := session.NewStore(sessionDir)
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
				fmt.Println(result)
				return nil
			}

			// Interactive REPL mode
			fmt.Printf("🤖 TinyCode (model: %s) — interactive mode\n", provider.Name())
			fmt.Println("Type your request, or /exit to quit. (Ctrl+C once to save & exit, twice to force)")

			inputCh := make(chan string)
			go func() {
				scanner := bufio.NewScanner(os.Stdin)
				for scanner.Scan() {
					inputCh <- scanner.Text()
				}
				close(inputCh)
			}()

				replLoop:
					for {
						fmt.Print("> ")
						select {
						case <-gracefulDone:
							break replLoop
						case line, ok := <-inputCh:
							if !ok {
								break replLoop
							}
							line = strings.TrimSpace(line)
							if line == "" {
								continue
							}
							if line == "/exit" || line == "/quit" {
								break replLoop
							}
							result, err := ag.Run(ctx, line)
							if err != nil {
								fmt.Printf("⚠️  Error: %v\n", err)
								continue
							}
							fmt.Println(result)
							fmt.Println()
						}
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
