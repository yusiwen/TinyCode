package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

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

// Build-time overrides (set via ldflags in Makefile)
var (
	Version   = "dev"
	CommitSHA = "unknown"
	BuildTime = "unknown"
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
	var resume string
	var listSessions bool
	var deleteSession string
	var exportSession string
	var searchSessions string

	rootCmd := &cobra.Command{
		Use:     "tinycode",
		Short:   "TinyCode - AI coding agent in Go",
		Version: fmt.Sprintf("%s (commit %s, built %s)", Version, CommitSHA, BuildTime),
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
			tlog.Info("main", "startup", "version", "dev")

			// Expand $HOME in sessionDir
			if sessionDir != "" {
				cfg.SessionDir = os.ExpandEnv(sessionDir)
			} else {
				cfg.SessionDir = os.ExpandEnv(cfg.SessionDir)
			}

			// Build provider registry from config
			var records []agent.ProviderRecord
			for i, pc := range cfg.Providers {
				key := os.Getenv(pc.APIKey())
				// CLI flags override the first provider's key (backward compat)
				if i == 0 && apiKey != "" {
					key = apiKey
				}
				modelName := pc.Model
				if i == 0 && model != "" {
					modelName = model
				}
				base := pc.BaseURL
				if i == 0 && baseURL != "" {
					base = baseURL
				}
				if base == "" {
					base = "https://api.deepseek.com"
				}

				var prov agent.LLMProvider
				switch pc.Type {
				case "ollama":
					prov = agent.NewOllamaProvider(base, modelName)
				default:
					// "openai" or unknown — use OpenAI-compatible provider
					prov = agent.NewOpenAIProvider(key, base, modelName)
				}
				records = append(records, agent.ProviderRecord{
					Name:     pc.Name,
					Provider: prov,
				})
			}

			// Fallback: if no providers configured, create a default one
			if len(records) == 0 {
				prov := agent.NewOpenAIProvider(apiKey, baseURL, model)
				records = append(records, agent.ProviderRecord{
					Name: "default", Provider: prov,
				})
			}

			provReg := agent.NewProviderRegistry(records)

			if cfg.LSP != nil && cfg.LSP.Enabled {
				lsp.Init(cfg.SessionDir)
			}

			// Wire sandbox config
			if cfg.Sandbox != nil && cfg.Sandbox.ProjectRoot != "" {
				tool.DefaultSandbox.ProjectRoot = cfg.Sandbox.ProjectRoot
			}
			if cfg.Sandbox != nil && len(cfg.Sandbox.DenyCommands) > 0 {
				tool.DefaultSandbox.CommandDenyList = append(
					tool.DefaultSandbox.CommandDenyList, cfg.Sandbox.DenyCommands...)
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

			ag := agent.New(provReg.Current())
			aCfg := reg.Current()
			ag.Config = aCfg
			ag.ShowThinking = true
			ag.CompressionThreshold = 500000 // 50% of 1M context for DeepSeek V4 Flash
			ag.ContextLength = 1000000      // DeepSeek V4 Flash supports 1M tokens

			// Load project context files (AGENTS.md, CLAUDE.md, .tinycode.md)
			if ctx := loadProjectContext(); ctx != "" {
				if aCfg.SystemPrompt != "" {
					aCfg.SystemPrompt += "\n\n<project-context>\n" + ctx + "\n</project-context>"
				} else {
					aCfg.SystemPrompt = "Project context:\n\n" + ctx
				}
			}
			// Inject available skills index into system prompt
			if skillIndex := skill.DiscoveredNames("."); skillIndex != "" {
				aCfg.SystemPrompt += skillIndex
			}
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
			ag.AddTool(lsp.ToolFactory(lsp.ToolGoToDefinition))
			ag.AddTool(lsp.ToolFactory(lsp.ToolFindReferences))
			ag.AddTool(lsp.ToolFactory(lsp.ToolHover))
			ag.AddTool(lsp.ToolFactory(lsp.ToolDocumentSymbols))
			ls := tool.LoadSkill()
			ag.AddTool(agent.Tool{
				Name: ls.Name, Description: ls.Description,
				Parameters: ls.Parameters, Execute: ls.Execute,
			})
			sm := tool.SkillManage()
			ag.AddTool(agent.Tool{
				Name: sm.Name, Description: sm.Description,
				Parameters: sm.Parameters, Execute: sm.Execute,
			})
			ag.AddTool(tool.SandboxAllowTool())

			tool.DefaultSandbox.ProjectRoot = "/home/yusiwen/git/ai/TinyCode"

			store := session.NewStore(cfg.SessionDir)
			sess := store.Create("default")
			ag.SessionStore = sess
			defer sess.Flush()

			if searchSessions != "" {
				infos := store.Search(searchSessions)
				if len(infos) == 0 {
					fmt.Println("No sessions matched.")
				} else {
					fmt.Printf("Found %d session(s) matching %q:\n", len(infos), searchSessions)
					for _, info := range infos {
						when := info.UpdatedAt.Format("2006-01-02 15:04")
						title := info.Title
						if title == "" {
							title = "(no title)"
						}
						fmt.Printf("  %-35s %s (%d msgs, %s)\n", info.ID, title, info.MessageCount, when)
					}
				}
				return nil
			}

			if deleteSession != "" {
				if err := store.Delete(deleteSession); err != nil {
					return fmt.Errorf("delete session: %w", err)
				}
				fmt.Printf("Deleted session: %s\n", deleteSession)
				return nil
			}

			if exportSession != "" {
				sess, err := store.Load(exportSession)
				if err != nil {
					return fmt.Errorf("load session: %w", err)
				}
				md := sess.ExportMarkdown()
				outPath := exportSession + ".md"
				if err := os.WriteFile(outPath, []byte(md), 0644); err != nil {
					return fmt.Errorf("write export: %w", err)
				}
				fmt.Printf("Exported session to: %s\n", outPath)
				return nil
			}

			if listSessions {
				infos := store.List()
				if len(infos) == 0 {
					fmt.Println("No saved sessions found.")
				} else {
					fmt.Println("Available sessions:")
					for _, info := range infos {
						when := info.UpdatedAt.Format("2006-01-02 15:04")
						title := info.Title
						if title == "" {
							title = "(no title)"
						}
						msgs := fmt.Sprintf("%d msgs", info.MessageCount)
						model := info.ModelName
						if model == "" {
							model = "?"
						}
						fmt.Printf("  %-35s %-50s %-12s %s\n",
							info.ID, title, msgs, when)
					}
				}
				return nil
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			prompt := ""
			if len(args) > 0 {
				prompt = args[0]
			}

			if prompt != "" {
				// One-shot mode
				fmt.Printf("🤖 TinyCode (model: %s)\n", provReg.CurrentName())
				result, err := ag.Run(ctx, prompt)
				if err != nil {
					return fmt.Errorf("agent error: %w", err)
				}
								if !ag.ContentStreamed {
									fmt.Println(result)
								}
				return nil
			}

			// Interactive TUI mode
			model := tui.NewTUI(ag, &cfg, reg, provReg, resume)
			p := tea.NewProgram(model, tea.WithMouseAllMotion())
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
	rootCmd.Flags().StringVar(&resume, "resume", "", "Resume a saved session by ID (e.g. TUI-20260607-235959)")
	rootCmd.Flags().BoolVar(&listSessions, "list-sessions", false, "List saved sessions")
	rootCmd.Flags().StringVar(&deleteSession, "delete-session", "", "Delete a saved session by ID")
	rootCmd.Flags().StringVar(&exportSession, "export-session", "", "Export a session as Markdown")
	rootCmd.Flags().StringVar(&searchSessions, "search-sessions", "", "Search session content")

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

// loadProjectContext reads project-level context files (AGENTS.md, CLAUDE.md, .tinycode.md)
// from the current working directory. Returns the concatenated content.
func loadProjectContext() string {
	// Search order: first match wins
	names := []string{"AGENTS.md", "CLAUDE.md", ".tinycode.md"}
	for _, name := range names {
		path := filepath.Join(".", name)
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return string(data)
		}
	}
	return ""
}
