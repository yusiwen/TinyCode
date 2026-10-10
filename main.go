package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

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
	"github.com/yusiwen/tinycode/types"
)

// Build-time overrides (set via ldflags in Makefile)
var (
	Version   = "0.0.7"
	CommitSHA = "unknown"
	BuildTime = "unknown"
)

func init() {
	godotenv.Load(filepath.Join(".tinycode", ".env"))
	godotenv.Load(filepath.Join(os.Getenv("HOME"), ".tinycode", ".env"))
}

func main() {
	// The confinement launcher is this same binary, re-exec'd. It is
	// intercepted before anything else — before cobra and before the dotenv
	// init above would matter — because its whole job is to apply a kernel
	// boundary and then replace the process with the command.
	if len(os.Args) > 1 && os.Args[1] == tool.SandboxLauncherCommand {
		os.Exit(tool.RunSandboxLauncher(os.Args[2:]))
	}
	if err := newRootCmd().Execute(); err != nil {
		log.Fatal(err)
	}
}

// newRootCmd builds the CLI command tree. It is a function rather than a
// package-level variable so tests can execute the real command with isolated
// flags (for example to prove that the informational commands never touch
// session files).
func newRootCmd() *cobra.Command {
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
	var listGrants bool
	var revokeGrant string

	rootCmd := &cobra.Command{
		Use:     "tinycode",
		Short:   "TinyCode - AI coding agent in Go",
		Version: fmt.Sprintf("%s (commit %s, built %s)", Version, CommitSHA, BuildTime),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.LoadConfig()

			// Initialize logger
			logDir := filepath.Join(expandPath(cfg.SessionDir), "..", "log")
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
				cfg.SessionDir = expandPath(sessionDir)
			} else {
				cfg.SessionDir = expandPath(cfg.SessionDir)
			}

			// Build provider registry from config
			provReg := buildProviderRegistry(&cfg, apiKey, model, baseURL)

			// Wire sandbox config first. The keys that can widen the fence come
			// from the user's own file: ./.tinycode/config.json is checked out
			// with the repository, so a project-local project_root or
			// allowed_paths would let that repository widen — or remove — the
			// boundary it is meant to run inside (issue #162). Deny rules a
			// project adds are kept: they only narrow.
			installSandboxBoundary()
			if cfg.Sandbox != nil && len(cfg.Sandbox.DenyCommands) > 0 {
				tool.DefaultSandbox.DenyCommands = append(
					tool.DefaultSandbox.DenyCommands, cfg.Sandbox.DenyCommands...)
			}
			applySandboxCapabilityPolicy(&cfg)

			// The language server workspace is the project, not the directory that
			// stores sessions — and it is the same root the fence was just built
			// around, so "inside the project" means one thing to both. Reading it
			// from the merged config is what let a repository point the language
			// server at / while the fence still called the repository the project.
			if cfg.LSP != nil && cfg.LSP.Enabled {
				lsp.Init(tool.DefaultSandbox.ProjectRoot)
			}

			reg := agent.NewRegistry()
			if err := applyAgentOverrides(reg, &cfg); err != nil {
				return err
			}
			if cfg.DefaultMode != "" {
				reg.Set(cfg.DefaultMode)
			}

			ag := agent.New(provReg.Current())
			aCfg := reg.Current()
			ag.Config = aCfg
			ag.ShowThinking = true

			// Context window, compression threshold and truncation limits come
			// from the config (defaults: 1M / 500K, see config.DefaultConfig).
			// The fallbacks keep the agent usable if a config zeroes them.
			ag.ContextLength = cfg.ContextLength
			if ag.ContextLength <= 0 {
				ag.ContextLength = 1000000
			}
			ag.CompressionThreshold = cfg.CompressionThreshold
			if ag.CompressionThreshold <= 0 {
				ag.CompressionThreshold = ag.ContextLength / 2
			}
			// Cumulative token budgets (0 = unlimited, the default).
			ag.BudgetTokensPerRun, ag.BudgetTokensPerSession = cfg.TokenBudgets()
			// Declared prices price a call whose route reported no charge. An
			// empty table prices nothing, which the agent reports as an unknown
			// cost rather than as zero.
			ag.PriceTable = priceTable(&cfg)
			if cfg.Truncation != nil {
				agent.SetTruncationConfig(cfg.Truncation.MaxLines, cfg.Truncation.MaxBytes, expandPath(cfg.Truncation.OutputDir))
			}
			if cfg.SearXNGURL != "" {
				tool.SetSearXNG(cfg.SearXNGURL)
			}

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

			// LSP tools. They read files through a language server, so they are
			// wrapped in the sandbox path gate (the lsp package cannot import
			// tool itself).
			ag.AddTool(tool.WithPathGate(lsp.ToolFactory(lsp.ToolGoToDefinition)))
			ag.AddTool(tool.WithPathGate(lsp.ToolFactory(lsp.ToolFindReferences)))
			ag.AddTool(tool.WithPathGate(lsp.ToolFactory(lsp.ToolHover)))
			ag.AddTool(tool.WithPathGate(lsp.ToolFactory(lsp.ToolDocumentSymbols)))

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
				Provider:  provReg.Current(),
				AllTools:  allToolList,
				BgTaskMgr: bgTaskMgr,
				// Sub-agents enforce the same limits against their own spend.
				BudgetTokensPerRun:     ag.BudgetTokensPerRun,
				BudgetTokensPerSession: ag.BudgetTokensPerSession,
				PriceTable:             ag.PriceTable,
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
			store := session.NewStore(cfg.SessionDir)

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
				// An exported transcript contains the whole conversation.
				if err := os.WriteFile(outPath, []byte(md), 0600); err != nil {
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

			// Persistent path grants: the answers that outlived their session.
			// They are listed here because "Always allow" is otherwise invisible
			// until someone reads the config file by hand — and a permission
			// that cannot be audited is one that gets left on.
			if listGrants {
				grants, err := config.ListAllowedPathGrants()
				if err != nil {
					return err
				}
				path, pathErr := config.UserConfigPath()
				if pathErr != nil {
					path = "(config path unavailable)"
				}
				if len(grants) == 0 {
					fmt.Printf("No persistent path grants in %s\n", path)
					return nil
				}
				fmt.Printf("Persistent path grants in %s:\n", path)
				for _, g := range grants {
					switch {
					case g.Legacy:
						fmt.Printf("  %-50s recorded before grants carried context\n", g.Path)
					default:
						project := g.Project
						if project == "" {
							project = "(unknown project)"
						}
						fmt.Printf("  %-50s granted %s from %s\n",
							g.Path, g.Granted.Local().Format("2006-01-02 15:04"), project)
					}
				}
				fmt.Println("Revoke one with --revoke-grant <path>")
				return nil
			}
			if revokeGrant != "" {
				removed, err := config.RevokeAllowedPathGrant(revokeGrant)
				if err != nil {
					return err
				}
				if removed {
					fmt.Printf("Revoked the persistent grant for %s\n", revokeGrant)
				} else {
					fmt.Printf("No persistent grant for %s\n", revokeGrant)
				}
				return nil
			}

			// The informational commands above returned already. Everything
			// below belongs to a real run: connecting MCP servers, wiring the
			// sandbox, and creating the session file that a run appends to.
			//
			// Connect MCP servers and register their tools
			var mcpCount int
			var mcpToolList []agent.Tool
			if len(cfg.MCPServers) > 0 {
				fmt.Print("  Connecting to MCP servers... ")
				mcpToolList, _ = tool.ConnectMCPServers(context.Background(), cfg.MCPServers)
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
			// Reap every MCP child (stdio) on the way out. Safe when none was
			// started, and idempotent.
			defer tool.CloseMCPServers()
			// The boundary root and the paths allowed beyond it were installed
			// from the user's own configuration at startup (installSandboxBoundary,
			// issue #162); nothing project-local can move them here.
			// …and the grants "Always allow" records in their own form. Nothing
			// read that form back before issue #155: the dialog appended a record
			// and the next start ignored it, so a permanent answer lasted exactly
			// as long as the process that made it.
			if err := loadPersistentGrants(); err != nil {
				tlog.Warn("sandbox", "grants_unreadable", "err", err)
			}
			applySandboxCapabilityPolicy(&cfg)

			// The session is created last: an informational command must never
			// create or flush a session file.
			sess := store.Create("default")
			sess.Containment = tool.ContainmentInfo().String()
			ag.SessionStore = sess
			defer sess.Flush()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			prompt := ""
			if len(args) > 0 {
				prompt = args[0]
			}

			// A one-shot run has no dialog, so the sandbox must refuse a denied
			// path instead of blocking forever on a permission request.
			tool.SetInteractive(prompt == "")

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
	rootCmd.Flags().BoolVar(&listGrants, "list-grants", false, "List persistent path grants and exit")
	rootCmd.Flags().StringVar(&revokeGrant, "revoke-grant", "", "Revoke a persistent path grant and exit")

	return rootCmd
}

// installSandboxBoundary puts the boundary in place for this process: the root
// the fence is built around and the paths allowed beyond it, from the user's own
// configuration first and the working directory second.
//
// A project-local ./.tinycode/config.json contributes neither key. It arrives
// with the repository, so it is attacker-controlled: project_root "/" removes the
// boundary altogether, and an allowed_paths entry hands the agent a path nobody
// allowed (issue #162). What such a file asked for is logged and dropped rather
// than ignored in silence, because a person who wrote that key deserves to know
// it did not take effect.
func installSandboxBoundary() {
	boundary := config.UserSandboxBoundary()
	if project := config.ProjectSandboxBoundary(); project.ProjectRoot != "" || len(project.AllowedPaths) > 0 {
		tlog.Warn("sandbox", "project_local_boundary_keys_ignored",
			"project_root", project.ProjectRoot,
			"allowed_paths", len(project.AllowedPaths),
			"reason", "only the user's own config may widen the sandbox")
	}

	if boundary.ProjectRoot != "" {
		tool.DefaultSandbox.ProjectRoot = boundary.ProjectRoot
	}
	// Pattern D: auto-allow the working directory. The parent directory is
	// deliberately NOT auto-allowed: it can be $HOME or "/", which would make
	// project-root containment meaningless.
	if cwd, err := os.Getwd(); err == nil {
		if tool.DefaultSandbox.ProjectRoot == "" {
			tool.DefaultSandbox.ProjectRoot = cwd
		}
		tool.DefaultSandbox.AutoAllowPaths = []string{cwd}
	}
	// Persistent allowed paths, from the user's own file for the same reason.
	for _, p := range boundary.AllowedPaths {
		tool.DefaultSandbox.AllowAlways(p)
	}
}

// applySandboxCapabilityPolicy turns the configured hard-boundary requirement
// into the tool package's policy, and records once what this host can enforce.
//
// The verdict is logged rather than assumed: a deployment that relies on the
// kernel boundary should see, in the run's own log, whether it got one. When
// the requirement is on and no kernel mechanism exists, the warning says what
// will happen next — every fenced operation refuses — so a refusal later in the
// run is not a surprise.
func applySandboxCapabilityPolicy(cfg *config.Config) {
	if cfg.Sandbox != nil && cfg.Sandbox.RequireHardBoundary {
		tool.SetRequireHardBoundary(true)
	}
	// The default is per platform and the user's own config may set it either
	// way; the decision (and the reason it is off on a host with no mechanism)
	// belongs to config.ResolveConfineCommands. When confinement is on, grant
	// the platform user cache root so a confined toolchain can write GOCACHE
	// and friends — the fence consumes the same list, which is the shared-root
	// rule — and the command's TMPDIR is pointed inside it (tool.confinedEnv).
	confined := config.ResolveConfineCommands(cfg, tool.CommandConfinementAvailable())
	tool.SetConfineCommands(confined)
	if confined {
		tool.DefaultSandbox.CacheRoots = tool.PlatformCacheRoots()
	}
	// Let a run freeze its policy from this configuration. Installed here and
	// not in an init so the wiring stays visible: the agent package cannot
	// import this one, and without the hook a run gets a policy with no roots.
	tool.InstallSandboxPolicyResolver()
	info := tool.ContainmentInfo()
	tlog.Info("sandbox", "containment", info.String())
	if tool.HardBoundaryRequired() && info.Level != tool.ContainmentKernel {
		tlog.Warn("sandbox", "hard_boundary_unavailable",
			"containment", info.String(),
			"effect", "fenced file operations will be refused")
	}
	if tool.ConfineCommands() {
		// Confining a subprocess is a different capability from confining our
		// own opens, so it gets its own verdict: a host can enforce one and not
		// the other, and the caller needs to know which one it configured.
		available := tool.CommandConfinementAvailable()
		tlog.Info("sandbox", "command_confinement", "requested", true, "available", available)
		if !available {
			tlog.Warn("sandbox", "command_confinement_unavailable",
				"effect", "every shell command will be refused rather than run unconfined")
		}
		// A confined command may write only under the writable roots, so with
		// none configured the boundary allows nothing and every command fails.
		// That is the configuration being incomplete rather than the boundary
		// misbehaving, and it is worth saying before the first command does.
		if roots := tool.PolicyFor(types.SandboxWorkspaceWrite).Roots; len(roots) == 0 {
			tlog.Warn("sandbox", "command_confinement_without_roots",
				"effect", "no writable root is configured, so confined commands cannot write anywhere; set sandbox.project_root")
		}
	} else if !tool.CommandConfinementAvailable() {
		// State it rather than leave commands silently unconfined: the
		// per-platform default is off here because the launcher would fail
		// closed, and /sandbox draws the same fact.
		tlog.Info("sandbox", "command_confinement_off",
			"reason", "this host has no subprocess mechanism; the platform default is off and commands run under the string checks only")
	}
}

// loadPersistentGrants puts the paths a person allowed with "Always allow" on
// the sandbox's allow-list, so the answer outlives the run that gave it.
//
// Only the user's own config file is read. The project-local
// ./.tinycode/config.json is attacker-controlled — a checked-out repository must
// not be able to grant itself a path outside its own root — so grants are read
// through the user-config helper rather than through the merged configuration.
// A missing file is not an error: the first run has nothing granted.
//
// The project recorded with a grant stays audit context (--list-grants shows
// it); the grant applies wherever the path is requested, which is what "always
// allow this path" says and what the older sandbox.allowed_paths list did.
func loadPersistentGrants() error {
	grants, err := config.ListAllowedPathGrants()
	if err != nil {
		return err
	}
	for _, grant := range grants {
		if grant.Path != "" {
			tool.DefaultSandbox.AllowAlways(grant.Path)
		}
	}
	return nil
}

// expandPath expands $VARS and a leading "~" in a configured path, so values
// like "~/.tinycode/sessions" behave the way users expect.
func expandPath(p string) string {
	p = os.ExpandEnv(p)
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
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

// resolveProviderKey returns the API key for one configured provider. An
// explicit api_key_env wins; otherwise UPPER(name)_API_KEY is used. When no
// explicit env var is configured and the derived one is empty, OPENAI_API_KEY is
// the generic fallback. The --api-key flag overrides the primary provider only,
// which preserves the single-provider behaviour without affecting the others.
func resolveProviderKey(pc config.ProviderRecordConfig, primary bool, apiKeyFlag string, getenv func(string) string) string {
	key := getenv(pc.APIKey())
	if key == "" && pc.APIKeyEnv == "" {
		key = getenv("OPENAI_API_KEY")
	}
	if primary && apiKeyFlag != "" {
		key = apiKeyFlag
	}
	return key
}

// providerRuntime resolves a provider's model and base URL. Only the primary
// provider accepts the --model/--base-url overrides, and an empty base URL falls
// back to the DeepSeek endpoint.
func providerRuntime(pc config.ProviderRecordConfig, primary bool, model, baseURL string) (string, string) {
	modelName := pc.Model
	if primary && model != "" {
		modelName = model
	}
	base := pc.BaseURL
	if primary && baseURL != "" {
		base = baseURL
	}
	if base == "" {
		base = "https://api.deepseek.com"
	}
	return modelName, base
}

// buildProviderRegistry turns the configured providers into a runtime registry.
// An empty configuration yields a single default OpenAI-compatible provider so
// the agent still starts.
func buildProviderRegistry(cfg *config.Config, apiKey, model, baseURL string) *agent.ProviderRegistry {
	var records []agent.ProviderRecord
	for i, pc := range cfg.Providers {
		primary := i == 0
		key := resolveProviderKey(pc, primary, apiKey, os.Getenv)
		modelName, base := providerRuntime(pc, primary, model, baseURL)

		var prov agent.LLMProvider
		switch pc.Type {
		case "ollama":
			prov = agent.NewOllamaProvider(base, modelName)
		default:
			// "openai" or unknown — use an OpenAI-compatible provider
			prov = agent.NewOpenAIProvider(key, base, modelName)
		}
		// A route is what a price is keyed on: the same model costs differently
		// through different routes, so the provider has to carry the name the
		// user gave it rather than only its type and model.
		if ri, ok := prov.(interface{ SetRouteInfo(string, string, string) }); ok {
			ri.SetRouteInfo(pc.Name, modelName, pc.CostCurrency)
		}
		records = append(records, agent.ProviderRecord{
			Name:     pc.Name,
			Provider: prov,
		})
	}

	// Fallback: if no providers are configured, create a default one.
	if len(records) == 0 {
		prov := agent.NewOpenAIProvider(apiKey, baseURL, model)
		prov.SetRouteInfo("default", model, "")
		records = append(records, agent.ProviderRecord{
			Name:     "default",
			Provider: prov,
		})
	}
	return agent.NewProviderRegistry(records)
}

// priceTable turns the declared rates in the configuration into the table the
// agent prices a call against. It returns nil when nothing is declared, which
// the agent reports as an unknown cost rather than as zero.
func priceTable(cfg *config.Config) *agent.PriceTable {
	if cfg == nil || cfg.Pricing == nil || len(cfg.Pricing.Prices) == 0 {
		return nil
	}
	table := agent.NewPriceTable(cfg.Pricing.Currency)
	for key, entry := range cfg.Pricing.Prices {
		table.Set(key, agent.Price{
			InputPerMillion:      entry.InputPerMillion,
			OutputPerMillion:     entry.OutputPerMillion,
			CacheReadPerMillion:  entry.CacheReadPerMillion,
			CacheWritePerMillion: entry.CacheWritePerMillion,
			ReasoningPerMillion:  entry.ReasoningPerMillion,
		})
	}
	return table
}

// applyAgentOverrides folds the config's per-agent overrides into the registry.
// Unknown agent names are ignored so a stale config entry cannot break startup,
// but an invalid permission effect is an error: silently dropping it would grant
// or deny the wrong tools.
func applyAgentOverrides(reg *agent.Registry, cfg *config.Config) error {
	for name, override := range cfg.Agents {
		aCfg, err := reg.Get(name)
		if err != nil {
			continue
		}
		if override.MaxSteps > 0 {
			aCfg.MaxSteps = override.MaxSteps
		}
		if override.SystemPrompt != "" {
			aCfg.SystemPrompt = override.SystemPrompt
		}
		// An explicit ruleset wins over the legacy tool lists.
		if len(override.Permissions) > 0 {
			rules := make(agent.Ruleset, 0, len(override.Permissions))
			for _, r := range override.Permissions {
				resource := r.Resource
				if resource == "" {
					resource = "*"
				}
				switch r.Effect {
				case "allow":
					rules = append(rules, agent.Rule{Action: r.Action, Resource: resource, Effect: agent.EffectAllow})
				case "deny":
					rules = append(rules, agent.Rule{Action: r.Action, Resource: resource, Effect: agent.EffectDeny})
				default:
					return fmt.Errorf("agents.%s.permissions: unknown effect %q (use \"allow\" or \"deny\")", name, r.Effect)
				}
			}
			aCfg.Permissions = rules
		} else if override.AllowedTools != nil || override.DeniedTools != nil {
			// Every built-in agent ships a Permissions ruleset, which takes
			// precedence over AllowedTools/DeniedTools. Translate the configured
			// lists into rules so they are not silently ignored (see
			// agent.TranslateToolLists).
			aCfg.Permissions = agent.TranslateToolLists(aCfg.Permissions, override.AllowedTools, override.DeniedTools)
		}
		if override.Model != "" {
			// Support "<provider>/<model>" and bare "<model>" formats
			if _, after, ok := strings.Cut(override.Model, "/"); ok {
				aCfg.Model = after // use model part, ignore provider for now
			} else {
				aCfg.Model = override.Model
			}
		}
	}
	return nil
}
