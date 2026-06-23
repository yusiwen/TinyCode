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
	"github.com/yusiwen/tinycode/types"
	"github.com/yusiwen/tinycode/tool"
	"github.com/yusiwen/tinycode/tui"
)

// Build-time overrides (set via ldflags in Makefile)
var (
	Version   = "0.0.4"
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
				// Fallback: if no explicit api_key_env was set and the derived key is
				// empty, try OPENAI_API_KEY as a generic fallback.
				if key == "" && pc.APIKeyEnv == "" {
					key = os.Getenv("OPENAI_API_KEY")
				}
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
				tool.DefaultSandbox.DenyCommands = append(
					tool.DefaultSandbox.DenyCommands, cfg.Sandbox.DenyCommands...)
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

			// ── Register tools (grouped by category) ──

			// Shell & File system
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

			// Line-level editing
			ed := tool.Edit()
			ag.AddTool(agent.Tool{
				Name: ed.Name, Description: ed.Description,
				Parameters: ed.Parameters, Execute: ed.Execute,
			})
			ap := tool.ApplyPatch()
			ag.AddTool(agent.Tool{
				Name: ap.Name, Description: ap.Description,
				Parameters: ap.Parameters, Execute: ap.Execute,
			})

			// Git
			gs := tool.GitStatus()
			ag.AddTool(agent.Tool{
				Name: gs.Name, Description: gs.Description,
				Parameters: gs.Parameters, Execute: gs.Execute,
			})
			gd := tool.GitDiff()
			ag.AddTool(agent.Tool{
				Name: gd.Name, Description: gd.Description,
				Parameters: gd.Parameters, Execute: gd.Execute,
			})
			gc := tool.GitCommit()
			ag.AddTool(agent.Tool{
				Name: gc.Name, Description: gc.Description,
				Parameters: gc.Parameters, Execute: gc.Execute,
			})
			gb := tool.GitBranch()
			ag.AddTool(agent.Tool{
				Name: gb.Name, Description: gb.Description,
				Parameters: gb.Parameters, Execute: gb.Execute,
			})
			gl := tool.GitLog()
			ag.AddTool(agent.Tool{
				Name: gl.Name, Description: gl.Description,
				Parameters: gl.Parameters, Execute: gl.Execute,
			})

			// Web tools
			ws := tool.WebSearch()
			ag.AddTool(agent.Tool{
				Name: ws.Name, Description: ws.Description,
				Parameters: ws.Parameters, Execute: ws.Execute,
			})
			we := tool.WebExtract()
			ag.AddTool(agent.Tool{
				Name: we.Name, Description: we.Description,
				Parameters: we.Parameters, Execute: we.Execute,
			})
			wb := tool.WebExtractBrowser()
			ag.AddTool(agent.Tool{
				Name: wb.Name, Description: wb.Description,
				Parameters: wb.Parameters, Execute: wb.Execute,
			})

			// LSP tools
			ag.AddTool(lsp.ToolFactory(lsp.ToolGoToDefinition))
			ag.AddTool(lsp.ToolFactory(lsp.ToolFindReferences))
			ag.AddTool(lsp.ToolFactory(lsp.ToolHover))
			ag.AddTool(lsp.ToolFactory(lsp.ToolDocumentSymbols))

			// Skills
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

			// Task tool — delegates to sub-agents (explore, general)
			bgTaskMgr := tool.NewBackgroundTaskManager()
			allToolList := ag.Tools // snapshot of tools registered so far
			taskTool := tool.TaskTool(&tool.TaskToolDeps{
				Provider: provReg.Current(),
				AllTools: allToolList,
				BgTaskMgr: bgTaskMgr,
				GetAgentConfig: func(name string) *agent.AgentConfig {
					cfg, err := reg.Get(name)
					if err != nil {
						return nil
					}
					return cfg
				},
			})
			ag.AddTool(agent.Tool{
				Name: taskTool.Name, Description: taskTool.Description,
				Parameters: taskTool.Parameters, Execute: taskTool.Execute,
			})
			// Task collect tool
			tc := tool.TaskCollectTool(bgTaskMgr)
			ag.AddTool(agent.Tool{
				Name: tc.Name, Description: tc.Description,
				Parameters: tc.Parameters, Execute: tc.Execute,
			})

			// Todo tool with shared store
			todoStore := tool.NewTodoStore()
			ag.TodoStorer = todoStore
			td := tool.Todo(todoStore)
			ag.AddTool(agent.Tool{
				Name: td.Name, Description: td.Description,
				Parameters: td.Parameters, Execute: td.Execute,
			})

			// Sandbox
			ag.AddTool(tool.SandboxAllowTool())
			// Wire LLM summarizer for web_extract (content >5000 chars)
			provider := provReg.Current()
			tool.SetSummarizer(func(ctx context.Context, content string) (string, error) {
				resp, err := provider.Chat(ctx, types.ChatRequest{
					Messages: []types.Message{
						{Role: types.RoleSystem, Content: "Summarize the following web page content in 3-5 sentences. Focus on key facts, data, and conclusions."},
						{Role: types.RoleUser, Content: content[:min(len(content), 8000)]},
					},
				})
				if err != nil {
					return "", err
				}
				return resp.Content, nil
			})
			// Connect MCP servers and register their tools
			var mcpCount int
			var mcpToolList []agent.Tool
			if len(cfg.MCPServers) > 0 {
				fmt.Print("  Connecting to MCP servers... ")
				mcpToolList, _ = tool.ConnectMCPServers(cfg.MCPServers)
				for _, mt := range mcpToolList {
					ag.AddTool(mt)
				}
				mcpCount = len(mcpToolList)
				if mcpCount > 0 {
					fmt.Printf("%d tools from %d server(s)\n", mcpCount, len(cfg.MCPServers))
					tlog.Info("main", "mcp tools registered", "count", mcpCount)
				} else {
					fmt.Println("no tools found")
				}
			}
			// Sandbox project root: config → CWD
			rootDir := ""
			if cfg.Sandbox != nil {
				rootDir = cfg.Sandbox.ProjectRoot
			}
			if rootDir == "" {
				cwd, err := os.Getwd()
				if err == nil {
					rootDir = cwd
				}
			}
			if rootDir != "" {
				tool.DefaultSandbox.ProjectRoot = rootDir
			}

			// Pattern D: auto-allow CWD and its parent directory
			if cwd, err := os.Getwd(); err == nil {
				parent := filepath.Dir(cwd)
				tool.DefaultSandbox.AutoAllowPaths = []string{cwd, parent}
			}
			// Load persistent allowed paths from config.json
			if cfg.Sandbox != nil {
				for _, p := range cfg.Sandbox.AllowedPaths {
					tool.DefaultSandbox.AllowAlways(p)
				}
			}

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
			model := tui.NewTUI(ag, &cfg, reg, provReg, todoStore, resume)
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
