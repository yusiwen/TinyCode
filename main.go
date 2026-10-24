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
	"github.com/yusiwen/tinycode/goal"
	"github.com/yusiwen/tinycode/internal/notify"
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
	var listTrajectories bool
	var showTrajectory string
	var searchSessions string
	var listGrants bool
	var revokeGrant string
	var undoLast bool

	rootCmd := &cobra.Command{
		Use:     "tinycode",
		Short:   "TinyCode - AI coding agent in Go",
		Version: fmt.Sprintf("%s (commit %s, built %s)", Version, CommitSHA, BuildTime),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Both configuration files are read once, here, and the value is
			// passed to every reader below. A reader that opened a layer again
			// reported a malformed file a second time, and two readers of one
			// start could disagree if the file changed in between (issue #231).
			layers := config.LoadLayers()
			cfg := layers.Config()

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
			provReg, err := buildProviderRegistry(&cfg, apiKey, model, baseURL)
			if err != nil {
				return err
			}

			// Wire sandbox config first. The keys that can widen the fence come
			// from the user's own file: ./.tinycode/config.json is checked out
			// with the repository, so a project-local project_root or
			// allowed_paths would let that repository widen — or remove — the
			// boundary it is meant to run inside (issue #162). Deny rules a
			// project adds are kept: they only narrow.
			installSandboxBoundary(layers)
			if cfg.Sandbox != nil && len(cfg.Sandbox.DenyCommands) > 0 {
				tool.DefaultSandbox.DenyCommands = append(
					tool.DefaultSandbox.DenyCommands, cfg.Sandbox.DenyCommands...)
			}
			applySandboxCapabilityPolicy(&cfg, layers.User)

			// The language server workspace is the project, not the directory that
			// stores sessions — and it is the same root the fence was just built
			// around, so "inside the project" means one thing to both. Reading it
			// from the merged config is what let a repository point the language
			// server at / while the fence still called the repository the project.
			if cfg.LSP != nil && cfg.LSP.Enabled {
				lsp.Init(tool.DefaultSandbox.ProjectRoot)
			}

			reg := agent.NewRegistry()
			if err := applyAgentOverrides(reg, &cfg, provReg); err != nil {
				return err
			}
			if cfg.DefaultMode != "" {
				reg.Set(cfg.DefaultMode)
			}

			ag := agent.New(provReg.Current())
			// A route pinned on an agent is resolved through the registry at call
			// time, so a session-wide switch does not silently override the pin
			// (roadmap A5).
			ag.Resolver = provReg
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
			// cost rather than as zero. A malformed schedule is fatal here: see
			// priceTable.
			table, err := priceTable(&cfg)
			if err != nil {
				return err
			}
			ag.PriceTable = table

			// Every run leaves a record, because a run that leaves nothing behind
			// cannot be compared with another one. The directory is registered
			// with the tool layer first, so a tool cannot write the evidence it
			// is being measured by. capture is taken from the user's own config
			// only when it names "full": see ResolveTrajectoryCapture.
			spec := cfg.TrajectorySpec()
			if spec.Enabled {
				capture, ignored := layers.User.ResolveTrajectoryCapture(&cfg)
				if ignored {
					tlog.Warn("config", "project_local_capture_full_ignored",
						"reason", "capture: full writes tool text to disk; only the user's own config may ask for it")
				}
				tool.SetTrajectoryRoot(spec.Dir)
				ag.Trajectory = agent.NewTrajectory(spec.Dir, capture)
				ag.TrajectoryMaxRuns = spec.MaxRuns
				ag.TrajectoryMaxBytes = spec.MaxBytes
			}
			if cfg.Truncation != nil {
				agent.SetTruncationConfig(cfg.Truncation.MaxLines, cfg.Truncation.MaxBytes, expandPath(cfg.Truncation.OutputDir))
			}
			if cfg.SearXNGURL != "" {
				tool.SetSearXNG(cfg.SearXNGURL)
			}
			// A finished background task announces itself, unless the user said
			// off. The absent section is not silence: see internal/notify for why
			// the default is on and terminal-only.
			channel, minDuration := cfg.NotifyPolicy()
			notify.Configure(notify.Config{
				Channel:     notify.Channel(channel),
				MinDuration: minDuration,
			})

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
				// A sub-agent resolves its own route (or the active provider when
				// it has none) at the moment it starts, rather than inheriting a
				// provider captured here at startup (roadmap A5).
				ResolveProvider: provReg.Resolve,
				AllTools:        allToolList,
				BgTaskMgr:       bgTaskMgr,
				// Sub-agents enforce the same limits against their own spend.
				BudgetTokensPerRun:     ag.BudgetTokensPerRun,
				BudgetTokensPerSession: ag.BudgetTokensPerSession,
				PriceTable:             ag.PriceTable,
				ParentRun:              ag.CurrentRun,
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

			// Goal service. The /goal command and the goal tool share it; the
			// session file is the only durable copy, written by the TUI.
			goals := goal.NewService()
			// One number for both creators: the /goal command and the goal tool.
			goals.DefaultRounds = cfg.GoalSettings().MaxRounds

			// Sandbox
			ag.AddTool(tool.SandboxAllowTool())
			// Wire LLM summarizer for web_extract (content >5000 chars). It
			// resolves the active route per call: a provider switch made after
			// startup must reach it (roadmap A5).
			tool.SetSummarizer(func(ctx context.Context, content string) (string, error) {
				provider, err := provReg.Resolve("")
				if err != nil {
					return "", err
				}
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

			if listTrajectories {
				return printTrajectories(&cfg, 20)
			}
			if showTrajectory != "" {
				return printTrajectory(&cfg, showTrajectory)
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

			if undoLast {
				// The undo is agnostic about sessions: a process started just to
				// undo has no conversation, and the newest call that has not been
				// undone is the one the user means (issue #234).
				call, ok, err := tool.LastCall("")
				if err != nil {
					return fmt.Errorf("undo: %w", err)
				}
				if !ok {
					fmt.Println("Nothing to undo: no tool call in this project has a recorded pre-image.")
					return nil
				}
				restored, err := tool.RestoreCall(cmd.Context(), call)
				for _, path := range restored {
					fmt.Printf("Restored: %s\n", path)
				}
				if err != nil {
					return fmt.Errorf("undo: %w", err)
				}
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
				grants, err := layers.User.AllowedPathGrants()
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
			if err := loadPersistentGrants(layers.User); err != nil {
				tlog.Warn("sandbox", "grants_unreadable", "err", err)
			}
			applySandboxCapabilityPolicy(&cfg, layers.User)

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

			// Interactive TUI mode. The goal tool exists only here: a goal is
			// session state, and a one-shot run has no session to keep it in.
			gt := tool.Goal(goals)
			ag.AddTool(agent.Tool{
				Name: gt.Name, Description: gt.Description,
				Parameters: gt.Parameters, Execute: gt.Execute,
			})

			model := tui.NewTUI(ag, &cfg, reg, provReg, todoStore, goals, resume)
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
	rootCmd.Flags().BoolVar(&listTrajectories, "trajectories", false, "List the newest run trajectories")
	rootCmd.Flags().StringVar(&showTrajectory, "trajectory", "", "Print one run trajectory as JSONL")
	rootCmd.Flags().StringVar(&searchSessions, "search-sessions", "", "Search session content")
	rootCmd.Flags().BoolVar(&listGrants, "list-grants", false, "List persistent path grants and exit")
	rootCmd.Flags().StringVar(&revokeGrant, "revoke-grant", "", "Revoke a persistent path grant and exit")
	rootCmd.Flags().BoolVar(&undoLast, "undo", false, "Restore the files the last tool call wrote, and exit")

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
//
// Both layers come from the one read the startup path made, so a malformed file
// is reported once (issue #231) and the boundary is built from the same bytes
// the merged configuration was.
func installSandboxBoundary(layers config.Layers) {
	boundary := layers.User.SandboxBoundary()
	if project := layers.Project.SandboxBoundary(); project.ProjectRoot != "" || len(project.AllowedPaths) > 0 {
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

	// Where the pre-images of tool writes are kept. It is wired here, beside the
	// boundary, because the two answer one question between them: what a tool may
	// write, and where the bytes it replaced are kept so that write can be undone
	// (issue #234). The store is outside the project root on purpose — the fence
	// above then confines it as well.
	if dir := layers.Config().CheckpointDir; dir != "" {
		tool.SetCheckpointRoot(expandPath(dir))
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
func applySandboxCapabilityPolicy(cfg *config.Config, user config.UserLayer) {
	if cfg.Sandbox != nil && cfg.Sandbox.RequireHardBoundary {
		tool.SetRequireHardBoundary(true)
	}
	// The default is per platform and the user's own config may set it either
	// way; the decision (and the reason it is off on a host with no mechanism)
	// belongs to config.ResolveConfineCommands. When confinement is on, grant
	// the platform user cache root so a confined toolchain can write GOCACHE
	// and friends — the fence consumes the same list, which is the shared-root
	// rule — and the command's TMPDIR is pointed inside it (tool.confinedEnv).
	confined := user.ResolveConfineCommands(cfg, tool.CommandConfinementAvailable())
	tool.SetConfineCommands(confined)
	if confined {
		// Create the installed toolchains' caches before reading the roots: a
		// *cold* module cache is a directory the confined command cannot create
		// for itself, and the Linux applier refuses a root it cannot open, so
		// granting a path that does not exist would break every command rather
		// than widen the boundary (issue #166).
		tool.EnsurePlatformCacheRoots()
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
func loadPersistentGrants(user config.UserLayer) error {
	grants, err := user.AllowedPathGrants()
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
//
// A prompt-cache policy that cannot be honoured stops startup instead of being
// dropped: a mode silently read as "off" would leave the user believing the
// prefix is cached. The error names the route and, through ResolvePromptCache,
// the accepted values (roadmap A4).
func buildProviderRegistry(cfg *config.Config, apiKey, model, baseURL string) (*agent.ProviderRegistry, error) {
	// A route is referenced by name (an agent pin, a failover entry, a restored
	// session), so two entries sharing one name would make every such reference
	// ambiguous. Unnamed entries stay legal: they simply cannot be referenced.
	named := make(map[string]int, len(cfg.Providers))
	for i, pc := range cfg.Providers {
		if pc.Name == "" {
			continue
		}
		if first, ok := named[pc.Name]; ok {
			return nil, fmt.Errorf("providers: %q is declared twice (entries #%d and #%d)",
				pc.Name, first+1, i+1)
		}
		named[pc.Name] = i
	}

	var records []agent.ProviderRecord
	for i, pc := range cfg.Providers {
		primary := i == 0
		key := resolveProviderKey(pc, primary, apiKey, os.Getenv)
		modelName, base := providerRuntime(pc, primary, model, baseURL)

		policy := agent.PromptCachePolicy{Mode: agent.PromptCacheOff}
		if pc.PromptCache != nil {
			var err error
			policy, err = agent.ResolvePromptCache(pc.PromptCache.Mode, pc.PromptCache.TTL)
			if err != nil {
				return nil, fmt.Errorf("provider %q: %w", providerLabel(pc, i), err)
			}
		}

		var prov agent.LLMProvider
		switch pc.Type {
		case "ollama":
			// The native endpoint accepts neither a routing key nor a
			// cache_control block, so an enabled policy there is a configuration
			// error, not something to ignore.
			if policy.Mode != agent.PromptCacheOff {
				return nil, fmt.Errorf("provider %q: prompt_cache is not supported by the native ollama backend",
					providerLabel(pc, i))
			}
			prov = agent.NewOllamaProvider(base, modelName)
		default:
			// "openai" or unknown — use an OpenAI-compatible provider
			p := agent.NewOpenAIProvider(key, base, modelName)
			p.SetPromptCache(policy)
			prov = p
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

	wired, err := wireFailover(cfg.Providers, records)
	if err != nil {
		return nil, err
	}
	records = wired

	// Fallback: if no providers are configured, create a default one.
	if len(records) == 0 {
		prov := agent.NewOpenAIProvider(apiKey, baseURL, model)
		prov.SetRouteInfo("default", model, "")
		records = append(records, agent.ProviderRecord{
			Name:     "default",
			Provider: prov,
		})
	}
	return agent.NewProviderRegistry(records), nil
}

// wireFailover validates the declared failover chains and wraps every route that
// has one.
//
// A route's list is flattened: a listed route's own list follows it, so two
// entries can describe one chain without repeating it. The walk that does the
// flattening is also where a cycle would run forever, so it refuses one at
// startup, as it refuses an unknown name, a self-reference and a name listed
// twice in one list.
//
// Each route is wrapped around its *raw* provider, never around another route's
// wrapper: a call follows one chain once, and a route used directly still gets
// its own chain.
func wireFailover(configs []config.ProviderRecordConfig, records []agent.ProviderRecord) ([]agent.ProviderRecord, error) {
	index := make(map[string]int, len(configs))
	for i, pc := range configs {
		if pc.Name == "" {
			continue
		}
		index[pc.Name] = i
	}
	raw := make([]agent.LLMProvider, len(records))
	for i := range records {
		raw[i] = records[i].Provider
	}

	chains := make([][]int, len(configs))
	for i, pc := range configs {
		if len(pc.Failover) == 0 {
			continue
		}
		label := providerLabel(pc, i)
		visiting := map[string]bool{}
		if pc.Name != "" {
			visiting[pc.Name] = true
		}
		seen := map[int]bool{}
		var chain []int
		var walk func(names []string, depth int) error
		walk = func(names []string, depth int) error {
			local := map[string]bool{}
			for _, name := range names {
				if name == "" {
					return fmt.Errorf("provider %q: failover names an empty route", label)
				}
				if local[name] {
					return fmt.Errorf("provider %q: failover lists %q twice", label, name)
				}
				local[name] = true
				j, ok := index[name]
				if !ok {
					return fmt.Errorf("provider %q: failover names unknown provider %q", label, name)
				}
				if depth == 0 && name == pc.Name {
					return fmt.Errorf("provider %q: failover names itself", label)
				}
				if visiting[name] {
					return fmt.Errorf("provider %q: failover cycle through %q", label, name)
				}
				visiting[name] = true
				if !seen[j] {
					seen[j] = true
					chain = append(chain, j)
				}
				if err := walk(configs[j].Failover, depth+1); err != nil {
					return err
				}
				// The name is off the current path, but it stays in the chain:
				// the same route reached from a second branch is a diamond to
				// flatten, not a cycle to refuse.
				delete(visiting, name)
			}
			return nil
		}
		if err := walk(pc.Failover, 0); err != nil {
			return nil, err
		}
		chains[i] = chain
	}

	for i, chain := range chains {
		if len(chain) == 0 {
			continue
		}
		fallbacks := make([]agent.LLMProvider, len(chain))
		for k, j := range chain {
			fallbacks[k] = raw[j]
		}
		records[i].Provider = agent.NewFailoverProvider(raw[i], fallbacks)
	}
	return records, nil
}

// providerLabel names a configuration entry in an error, falling back to its
// position when the user did not give it a name.
func providerLabel(pc config.ProviderRecordConfig, index int) string {
	if pc.Name != "" {
		return pc.Name
	}
	return fmt.Sprintf("#%d", index+1)
}

// priceTable turns the declared rates in the configuration into the table the
// agent prices a call against. It returns nil when nothing is declared, which
// the agent reports as an unknown cost rather than as zero.
//
// A malformed window is an error rather than a warning. The rates decide what the
// user is told they spent, and a schedule that overlaps, covers the whole week or
// leaves a lane unpriced has no single right answer — so startup names the entry
// and the window and stops, the way an invalid permission effect does. A refused
// entry is stored as nothing at all, which is why the call it would have priced is
// reported unknown instead of being priced by a guess (issue #225).
func priceTable(cfg *config.Config) (*agent.PriceTable, error) {
	if cfg == nil || cfg.Pricing == nil || len(cfg.Pricing.Prices) == 0 {
		return nil, nil
	}
	table := agent.NewPriceTable(cfg.Pricing.Currency)
	for key, entry := range cfg.Pricing.Prices {
		windows := make([]agent.PriceWindow, 0, len(entry.Windows))
		for _, declared := range entry.Windows {
			window, err := agent.NewWindow(declared.Weekdays, declared.From, declared.To, windowPrice(declared))
			if err != nil {
				return nil, fmt.Errorf("pricing %s: %w", key, err)
			}
			windows = append(windows, window)
		}
		if err := table.SetEntry(key, entryPrice(entry), windows); err != nil {
			return nil, err
		}
	}
	return table, nil
}

// entryPrice and windowPrice map the configuration's two lane spellings onto the
// agent's. They are written out field by field rather than through a shared helper
// so that a lane added to one spelling has to be added here, and the lane test in
// pricing_config_test.go holds each lane to its own rate.
func entryPrice(entry config.PriceEntry) agent.Price {
	return agent.Price{
		InputPerMillion:      entry.InputPerMillion,
		OutputPerMillion:     entry.OutputPerMillion,
		CacheReadPerMillion:  entry.CacheReadPerMillion,
		CacheWritePerMillion: entry.CacheWritePerMillion,
		ReasoningPerMillion:  entry.ReasoningPerMillion,
	}
}

func windowPrice(entry config.PriceWindowEntry) agent.Price {
	return agent.Price{
		InputPerMillion:      entry.InputPerMillion,
		OutputPerMillion:     entry.OutputPerMillion,
		CacheReadPerMillion:  entry.CacheReadPerMillion,
		CacheWritePerMillion: entry.CacheWritePerMillion,
		ReasoningPerMillion:  entry.ReasoningPerMillion,
	}
}

// applyAgentOverrides folds the config's per-agent overrides into the registry.
// Unknown agent names are ignored so a stale config entry cannot break startup,
// but an invalid permission effect is an error: silently dropping it would grant
// or deny the wrong tools.
//
// A model of the form "<route>/<model>" also selects the provider: the route half
// is resolved against the registry here so a typo is a startup error rather than a
// run that quietly uses another provider (roadmap A5).
func applyAgentOverrides(reg *agent.Registry, cfg *config.Config, routes agent.RouteResolver) error {
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
			route, model, err := agent.ParseRoute(override.Model)
			if err != nil {
				return fmt.Errorf("agents.%s.model: %w", name, err)
			}
			if route != "" {
				if routes == nil {
					return fmt.Errorf("agents.%s.model: route %q cannot be resolved without a provider registry",
						name, route)
				}
				if _, err := routes.Resolve(route); err != nil {
					return fmt.Errorf("agents.%s.model: %w", name, err)
				}
			}
			aCfg.Route = route
			aCfg.Model = model
		}
	}
	return nil
}

// printTrajectories lists the newest runs, newest first.
//
// It reads the files rather than an index, so the list is the format's first
// consumer: a field the writer stopped carrying, or a shape a reader cannot
// parse, shows up here before it shows up in an analysis script somebody wrote.
func printTrajectories(cfg *config.Config, limit int) error {
	spec := cfg.TrajectorySpec()
	runs, err := agent.ListRuns(spec.Dir, limit)
	if err != nil {
		return fmt.Errorf("list trajectories: %w", err)
	}
	if len(runs) == 0 {
		fmt.Printf("No trajectories in %s.\n", spec.Dir)
		return nil
	}
	fmt.Printf("Trajectories in %s (newest %d):\n", spec.Dir, len(runs))
	for _, run := range runs {
		stop := run.Stop
		if !run.Complete {
			stop = "incomplete"
		}
		cost := "-"
		if len(run.Cost) > 0 {
			cost = run.Cost.String()
		}
		if run.CostUnknown > 0 {
			cost = fmt.Sprintf("%s (+%d unpriced)", cost, run.CostUnknown)
		}
		fmt.Printf("  %-24s %-12s %2d steps %7.1fs %6d tok %s\n    %s\n",
			run.Run, stop, run.Steps, run.Duration.Seconds(), run.TokensTotal(), cost, run.Path)
	}
	return nil
}

// printTrajectory writes one run's file to stdout unchanged.
//
// The id is checked against the store's own naming before it becomes a path: an
// id such as "../../etc/passwd" would otherwise read a file this store never
// wrote, and the whole point of the record is that it is the record.
func printTrajectory(cfg *config.Config, id string) error {
	if !agent.ValidRunID(id) {
		return fmt.Errorf("trajectory: %q is not a run id", id)
	}
	spec := cfg.TrajectorySpec()
	path := filepath.Join(spec.Dir, id+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read trajectory %s: %w", id, err)
	}
	_, err = os.Stdout.Write(data)
	return err
}
