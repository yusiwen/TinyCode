package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/lsp"
	"github.com/yusiwen/tinycode/session"
	"github.com/yusiwen/tinycode/skill"
	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/tool"
	"github.com/yusiwen/tinycode/tui"
)

func init() {
	godotenv.Load(filepath.Join(".tinycode", ".env"))
	godotenv.Load(filepath.Join(os.Getenv("HOME"), ".tinycode", ".env"))
}

func main() {
	var apiKey string
	var baseURL string
	var model string
	var sessionDir string
	var logLevel string

	rootCmd := &cobra.Command{
		Use:   "tinycode",
		Short: "TinyCode - AI coding agent in Go",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.LoadConfig()

			// Initialize logger
			logDir := filepath.Join(os.ExpandEnv(cfg.SessionDir), "..", "log")
			lvl := tlog.ParseLevel(cfg.LogLevel)
			if envLevel := os.Getenv("LOG_LEVEL"); envLevel != "" {
				lvl = tlog.ParseLevel(envLevel)
			}
			if logLevel != "" {
				lvl = tlog.ParseLevel(logLevel)
			}
			tlog.Init(logDir, lvl)
			tlog.Info("main", "startup", "version", "dev", "log_level", logLevel)

			// Expand $HOME in sessionDir
			if sessionDir != "" {
				cfg.SessionDir = os.ExpandEnv(sessionDir)
			} else {
				cfg.SessionDir = os.ExpandEnv(cfg.SessionDir)
			}

			// Resolve provider settings
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

			provider := agent.NewDeepSeekProvider(apiKey, baseURL, model)
			if cfg.LSP.Enabled {
				lsp.Init(cfg.SessionDir)
			}

			reg := agent.NewRegistry()
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
			if cfg.DefaultMode != "" {
				reg.Set(cfg.DefaultMode)
			}

			ag := agent.New(provider)
			ag.Config = reg.Current()
			ag.ShowThinking = true
			if cfg.ShowThinking != nil {
				ag.ShowThinking = *cfg.ShowThinking
			}
			if cfg.Verbose != nil {
				ag.Verbose = *cfg.Verbose
			}

			// Register tools
			ag.AddTool(agent.Tool{
				Name: tool.Bash().Name, Description: tool.Bash().Description,
				Parameters: tool.Bash().Parameters, Execute: tool.Bash().Execute,
			})
			ag.AddTool(agent.Tool{
				Name: tool.ReadFile().Name, Description: tool.ReadFile().Description,
				Parameters: tool.ReadFile().Parameters, Execute: tool.ReadFile().Execute,
			})
			ag.AddTool(agent.Tool{
				Name: tool.WriteFile().Name, Description: tool.WriteFile().Description,
				Parameters: tool.WriteFile().Parameters, Execute: tool.WriteFile().Execute,
			})
			ag.AddTool(agent.Tool{
				Name: tool.SearchFiles().Name, Description: tool.SearchFiles().Description,
				Parameters: tool.SearchFiles().Parameters, Execute: tool.SearchFiles().Execute,
			})
			ag.AddTool(skill.NewCodeReviewSkill().ToTool())
			ag.AddTool(skill.NewGitCommitSkill().ToTool())
			ag.AddTool(lsp.ToolFactory(lsp.ToolGoToDefinition))
			ag.AddTool(lsp.ToolFactory(lsp.ToolFindReferences))
			ag.AddTool(lsp.ToolFactory(lsp.ToolHover))
			ag.AddTool(lsp.ToolFactory(lsp.ToolDocumentSymbols))
			ag.AddTool(tool.SandboxAllowTool())

			tool.DefaultSandbox.ProjectRoot = "/home/yusiwen/git/ai/TinyCode"

			store := session.NewStore(cfg.SessionDir)
			sess := store.Create("default")
			ag.SessionStore = sess

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			gracefulDone := make(chan struct{})
			go func() {
				<-sigCh
				fmt.Println("\n\nSaving session...")
				sess.Flush()
				cancel()
				close(gracefulDone)
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
				if !ag.ContentStreamed {
					printMarkdown(result, cfg.GlamourStyle)
				}
				return nil
			}

			// Interactive TUI mode
			model := tui.NewTUI(ag, &cfg, reg)
			p := tea.NewProgram(model)
			if _, err := p.Run(); err != nil {
				return err
			}

			fmt.Println("\nBye!")
			return nil
		},
	}

	rootCmd.Flags().StringVar(&apiKey, "api-key", "", "API key")
	rootCmd.Flags().StringVar(&baseURL, "base-url", "", "API base URL")
	rootCmd.Flags().StringVar(&model, "model", "", "Model name")
	rootCmd.Flags().StringVar(&sessionDir, "session-dir", "", "Session directory")
	rootCmd.Flags().StringVar(&logLevel, "log-level", "", "Log level")

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
