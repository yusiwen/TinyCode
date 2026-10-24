// Package config provides the unified configuration for TinyCode.
// Config files are loaded in order (later overrides earlier):
//  1. Code defaults (hardcoded)
//  2. ~/.tinycode/config.json (user global)
//  3. ./.tinycode/config.json (project local)
//  4. Environment variables / CLI flags (highest priority)
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ProviderRecordConfig holds one provider definition.
type ProviderRecordConfig struct {
	Name string `json:"name,omitempty"`
	// Type selects the wire protocol: "openai" (the default, and the shape every
	// OpenAI-compatible gateway speaks), "ollama", "anthropic", "gemini",
	// "bedrock" or "azure". A native type exists because the compatible shape
	// drops the vendor's own reasoning stream and its own billing lanes; an
	// unknown type falls back to "openai", which is what the single-provider
	// release did before any other backend existed (roadmap A6).
	Type      string `json:"type,omitempty"`
	Model     string `json:"model,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"` // env var name for API key
	// APIVersion is the Azure OpenAI REST version, for type "azure" only. It
	// defaults to the pinned stable version when absent.
	APIVersion string `json:"api_version,omitempty"`
	// Region is the AWS region for type "bedrock". It wins over AWS_REGION and
	// AWS_DEFAULT_REGION, which are read when it is absent.
	Region string `json:"region,omitempty"`
	// ThinkingBudgetTokens asks the route to reason before answering, in the
	// vendors that charge for it (Anthropic, Gemini, Bedrock). Zero means the
	// route's own default: no thinking request is sent. The value is passed
	// through unchanged — each vendor has its own minimum and maximum — and a
	// vendor that rejects it fails the call with the API's message, because
	// guessing a budget would spend money on a value nobody chose.
	ThinkingBudgetTokens int `json:"thinking_budget_tokens,omitempty"`
	// PromptCache tells this route how to cache its prompt prefix (roadmap A4).
	// Absent means "send no cache hints": DeepSeek, OpenAI and Gemini cache a
	// stable prefix automatically, while Anthropic-style routes only cache what
	// carries an explicit breakpoint. The mode and ttl are validated when the
	// provider is built, so a value that cannot be honoured is a startup error
	// rather than a silent no-op.
	PromptCache *PromptCacheConfig `json:"prompt_cache,omitempty"`
	// CostCurrency names the unit this route bills a *reported* charge in, e.g.
	// "credits" for an account that bills in its own credits. Empty means the
	// route's unit is unknown, and an amount is then displayed without one
	// rather than labelled with a unit nobody stated.
	CostCurrency string `json:"cost_currency,omitempty"`
	// Failover lists the routes to try, in order, when this one fails in a way
	// another route could survive: a rate limit, a server error, a timeout, a
	// network error, or a stream that went silent. It is off when empty, which is
	// the default, because moving a call to another route spends money on a
	// decision the user did not make. Names are validated when the providers are
	// built: an unknown name, a self-reference, a duplicate and a cycle are
	// startup errors. A listed route's own list follows it, so two entries can
	// describe one chain; the fallback attempt asks for the fallback's own
	// configured model, never the model the primary was asked for (roadmap A5).
	Failover []string `json:"failover,omitempty"`
}

// PromptCacheConfig is one route's prompt-cache policy.
//
// Mode is "off" (the default), "auto" or "explicit". Auto sends only a stable
// prompt_cache_key, which improves routing on OpenAI before GPT-5.6 and is the
// sticky-routing key on OpenRouter; explicit additionally marks the end of the
// leading system message with cache_control. TTL is "5m" (the provider default)
// or "1h" and is only meaningful with explicit.
type PromptCacheConfig struct {
	Mode string `json:"mode,omitempty"`
	TTL  string `json:"ttl,omitempty"`
}

// TruncationConfig holds tool output truncation settings.
type TruncationConfig struct {
	MaxLines  int    `json:"max_lines,omitempty"`
	MaxBytes  int    `json:"max_bytes,omitempty"`
	OutputDir string `json:"output_dir,omitempty"`
}

// PricingConfig declares what a route charges, for the routes that do not report
// a charge themselves.
//
// The key of Prices is "<route>/<model>", where the route is the provider's name
// in this file — a price belongs to a billing endpoint, not to a model name,
// because the same model costs differently through a reseller, a gateway and the
// vendor's own API. "*" is allowed for the route, the model or both, so one rate
// can cover a whole route.
//
// Currency names the unit the rates are in (e.g. "USD"). It is deliberately not
// defaulted: a number with the wrong unit is worse than a number whose unit the
// user has to state. It is also independent of a provider's own
// cost_currency — an account billed in credits can still be priced from an
// invoice in dollars, and the two are accumulated apart rather than added.
type PricingConfig struct {
	Currency string                `json:"currency,omitempty"`
	Prices   map[string]PriceEntry `json:"prices,omitempty"`
}

// PriceEntry is a rate per million tokens, per billing lane.
//
// A zero detail rate falls back to the lane it is carved out of: cached and
// written input to the input rate, reasoning to the output rate. That is how a
// route with one input price and one output price is declared, and it is why a
// zero here is "not separately priced" rather than "free".
//
// These are the entry's own rates, and they apply at every instant a window does
// not cover: a route that bills peak and off-peak declares its steady (or
// off-peak) rates here and the peak slice in Windows. An entry with no windows
// behaves exactly as it did before windows existed.
type PriceEntry struct {
	InputPerMillion      float64 `json:"input_per_million"`
	OutputPerMillion     float64 `json:"output_per_million"`
	CacheReadPerMillion  float64 `json:"cache_read_per_million,omitempty"`
	CacheWritePerMillion float64 `json:"cache_write_per_million,omitempty"`
	ReasoningPerMillion  float64 `json:"reasoning_per_million,omitempty"`
	// Windows are the slices of the week whose rates replace these. They are
	// validated when the table is built (issue #225): an overlapping set, one that
	// covers the whole week, or one that leaves a lane this entry declares at zero
	// is a configuration error, not a guess.
	Windows []PriceWindowEntry `json:"windows,omitempty"`
}

// PriceWindowEntry is a slice of the week during which an entry charges other
// rates.
//
// Weekdays name days as mon…sun, or their full names, in any case. From and To are
// HH:MM in UTC: From is inclusive, To is exclusive, and 00:00 or 24:00 means
// midnight at the end of the day, which is how the published schedules write
// "until midnight" (LiteLLM's DeepSeek windows say 10:00-00:00). A window may not
// cross midnight: its minutes would have no single weekday, and writing it as two
// windows says the same thing without the ambiguity.
//
// The lanes are spelled exactly as PriceEntry's, and a window has to repeat every
// lane its entry declares. A lane left at zero inside a window would silently
// charge the entry's rate there — half price, in exactly one lane — which is the
// error this tier exists to prevent, so the loader refuses it instead.
type PriceWindowEntry struct {
	Weekdays             []string `json:"weekdays"`
	From                 string   `json:"from"`
	To                   string   `json:"to"`
	InputPerMillion      float64  `json:"input_per_million"`
	OutputPerMillion     float64  `json:"output_per_million"`
	CacheReadPerMillion  float64  `json:"cache_read_per_million,omitempty"`
	CacheWritePerMillion float64  `json:"cache_write_per_million,omitempty"`
	ReasoningPerMillion  float64  `json:"reasoning_per_million,omitempty"`
}

// BudgetConfig bounds what a run and a session may spend, in tokens, as the
// provider reports them.
//
// Zero means unlimited, which is the default: a budget is something a user opts
// into, not a policy this project imposes. The two limits are independent — a
// run inside a fresh session is still bounded by the run limit, and a long-lived
// session stops at the session limit even when each run is small.
type BudgetConfig struct {
	MaxTokensPerRun     int `json:"max_tokens_per_run,omitempty"`
	MaxTokensPerSession int `json:"max_tokens_per_session,omitempty"`
}

// GoalConfig is the continuation-driver surface (roadmap D7).
//
// MaxGoalRounds bounds how many rounds one goal may run. One round is a whole
// turn — dozens of tool calls — so this is a budget on continuation, not on a
// single turn; the token budgets are what bound the money.
//
// IdleGraceSeconds is how long the session must stay idle before an automatic
// round starts. It exists so a human who was already typing gets in first: a run
// refuses concurrent input, so a round that starts while a draft is open would
// lock the user out for its whole duration. Zero is not a meaningful value here
// (it would reintroduce exactly that), and an absent or zero field takes the
// default.
//
// NoProgressRounds blocks a goal after that many consecutive rounds made no tool
// call. It is the guard a round cap cannot provide: a model that only narrates
// would otherwise spend every round of the budget saying nothing.
type GoalConfig struct {
	MaxGoalRounds    int `json:"max_goal_rounds,omitempty"`
	IdleGraceSeconds int `json:"idle_grace_seconds,omitempty"`
	NoProgressRounds int `json:"no_progress_rounds,omitempty"`
}

// GoalPolicy is the effective continuation policy: the configured values with
// the built-in defaults filled in. Callers read this rather than the section, so
// a layer that sets one field does not have to repeat the others.
type GoalPolicy struct {
	MaxRounds        int
	IdleGraceSeconds int
	NoProgressRounds int
}

// Defaults the policy falls back to. They are the shipped values, and the
// shipped example names them so a reader can tell which knob is which.
const (
	defaultMaxGoalRounds    = 256
	defaultIdleGraceSeconds = 2
	defaultNoProgressRounds = 3
)

// GoalSettings resolves the continuation policy for this configuration.
func (c Config) GoalSettings() GoalPolicy {
	policy := GoalPolicy{
		MaxRounds:        defaultMaxGoalRounds,
		IdleGraceSeconds: defaultIdleGraceSeconds,
		NoProgressRounds: defaultNoProgressRounds,
	}
	if c.Goal == nil {
		return policy
	}
	if c.Goal.MaxGoalRounds > 0 {
		policy.MaxRounds = c.Goal.MaxGoalRounds
	}
	if c.Goal.IdleGraceSeconds > 0 {
		policy.IdleGraceSeconds = c.Goal.IdleGraceSeconds
	}
	// Zero would disable the guard; a configured value must be positive, so an
	// absent or zero field takes the default rather than silently removing it.
	if c.Goal.NoProgressRounds > 0 {
		policy.NoProgressRounds = c.Goal.NoProgressRounds
	}
	return policy
}

// NotifyConfig declares when a finished task should announce itself (roadmap D5).
//
// Channel is one of "terminal" (the default: an escape the terminal may render as
// a notification, invisible if it does not), "desktop" (the platform's own
// notifier, opt-in because a notification-centre entry per long run is a
// different kind of intrusion), "both", or "off".
//
// MinDurationSeconds is how long something has to take first. It defaults to 30
// when unset; an explicit 0 announces everything. A threshold rather than a focus
// check, because there is no portable way to ask a terminal whether it has focus.
type NotifyConfig struct {
	Channel            string `json:"channel,omitempty"`
	MinDurationSeconds int    `json:"min_duration_seconds,omitempty"`
}

// TrajectoryConfig declares where a run's record goes and how much of it is
// kept. It is on by default: a run that leaves nothing behind cannot be
// compared with another one, and F1 (an evaluation framework) has nothing to
// read. Capture defaults to metadata — digests and lengths, no text — because
// this writes to disk on every run and tool arguments carry secrets.
type TrajectoryConfig struct {
	// Enabled is a pointer so "off" can be said out loud: an absent field means
	// the default, which is on.
	Enabled *bool  `json:"enabled,omitempty"`
	Dir     string `json:"dir,omitempty"`
	// Capture is "metadata" (digests and lengths) or "full" (text as well, up to
	// a bound). An unknown value reads as metadata rather than failing a start:
	// a typo must not be a reason to record less safely.
	Capture string `json:"capture,omitempty"`
	// MaxRuns and MaxBytes bound the store; zero means the defaults.
	MaxRuns  int   `json:"max_runs,omitempty"`
	MaxBytes int64 `json:"max_bytes,omitempty"`
}

// The defaults for the two bounds, and the numbers the agent package falls back
// to when it is handed zero. They are asserted equal in trajectory_config_test.go
// so the pair cannot drift.
const (
	DefaultTrajectoryMaxRuns  = 200
	DefaultTrajectoryMaxBytes = 64 << 20
)

// TrajectorySpec is the effective recording configuration, with every default
// applied and every unknown value resolved.
type TrajectorySpec struct {
	Enabled  bool
	Dir      string
	Capture  string
	MaxRuns  int
	MaxBytes int64
}

// Capture modes. Agent copies of these strings are what the record writes; the
// config layer validates against them.
const (
	TrajectoryCaptureMetadata = "metadata"
	TrajectoryCaptureFull     = "full"
)

// TrajectorySpec resolves the section, filling in the defaults.
func (c Config) TrajectorySpec() TrajectorySpec {
	spec := TrajectorySpec{
		Enabled:  true,
		Capture:  TrajectoryCaptureMetadata,
		MaxRuns:  DefaultTrajectoryMaxRuns,
		MaxBytes: DefaultTrajectoryMaxBytes,
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		spec.Dir = filepath.Join(home, ".tinycode", "trajectories")
	}
	if c.Trajectory == nil {
		return spec
	}
	section := c.Trajectory
	if section.Enabled != nil {
		spec.Enabled = *section.Enabled
	}
	if section.Dir != "" {
		spec.Dir = section.Dir
	}
	if strings.EqualFold(section.Capture, TrajectoryCaptureFull) {
		spec.Capture = TrajectoryCaptureFull
	}
	if section.MaxRuns > 0 {
		spec.MaxRuns = section.MaxRuns
	}
	if section.MaxBytes > 0 {
		spec.MaxBytes = section.MaxBytes
	}
	if spec.Dir == "" {
		spec.Enabled = false
	}
	return spec
}

// AgentRule mirrors agent.Rule for configuration files. Keeping a local copy
// avoids a config → agent dependency; main.go translates it.
type AgentRule struct {
	Action   string `json:"action"`             // tool name or "*"
	Resource string `json:"resource,omitempty"` // ignored today, defaults to "*"
	Effect   string `json:"effect"`             // "allow" | "deny"
}

// AgentOverride holds per-agent configuration overrides.
type AgentOverride struct {
	MaxSteps     int      `json:"max_steps,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	DeniedTools  []string `json:"denied_tools,omitempty"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Model        string   `json:"model,omitempty"` // "<provider>/<model>", e.g. "deepseek/deepseek-v4-pro"
	// Permissions replaces the agent's built-in ruleset when set (last match
	// wins, like the built-in rules). It takes precedence over
	// allowed_tools/denied_tools.
	Permissions []AgentRule `json:"permissions,omitempty"`
}

// APIKey returns the env var name to look up for this provider's API key.
// Priority: api_key_env (if set) → UPPER(NAME)_API_KEY → the type's convention.
// A native backend falls back to its vendor's own variable, so an Anthropic
// route named "claude" does not silently offer an OpenAI key to api.anthropic.com
// (roadmap A6).
func (p ProviderRecordConfig) APIKey() string {
	if p.APIKeyEnv != "" {
		return p.APIKeyEnv
	}
	if p.Name != "" {
		return strings.ToUpper(p.Name) + "_API_KEY"
	}
	return p.DefaultAPIKeyEnv()
}

// DefaultAPIKeyEnv is the conventional credential variable for this provider
// type, used when the route has no name and no explicit api_key_env.
func (p ProviderRecordConfig) DefaultAPIKeyEnv() string {
	switch p.Type {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	case "azure":
		return "AZURE_OPENAI_API_KEY"
	default:
		return "OPENAI_API_KEY"
	}
}

type LSPConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

// SandboxConfig holds tool sandbox configuration from config.json.
type SandboxConfig struct {
	ProjectRoot  string   `json:"project_root,omitempty"`
	DenyCommands []string `json:"deny_commands,omitempty"`
	AllowedPaths []string `json:"allowed_paths,omitempty"`

	// RequireHardBoundary refuses file operations that would be enforced by
	// in-process checks alone. It is for a deployment that must not run with a
	// weaker boundary than the kernel provides, and it fails closed: on a host
	// with no kernel mechanism every fenced operation is refused instead of
	// being silently allowed.
	RequireHardBoundary bool `json:"require_hard_boundary,omitempty"`

	// ConfineCommands runs shell commands under the kernel file boundary
	// instead of only under the string checks. It is a pointer so "unset"
	// differs from an explicit false: the default is decided per platform by
	// ResolveConfineCommands — on where this host can confine a subprocess, off
	// where it cannot — and a user who needs the wider boundary can set it to
	// false in their own config.
	ConfineCommands *bool `json:"confine_commands,omitempty"`
}

// MCPServerConfig defines a single MCP server to connect to.
type MCPServerConfig struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"` // "stdio" or "http"
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`     // for http transport
	Headers   map[string]string `json:"headers,omitempty"` // for http transport
}

// Config is the top-level configuration structure.
type Config struct {
	DefaultMode  string                   `json:"default_mode,omitempty"`
	ShowThinking *bool                    `json:"show_thinking,omitempty"`
	Verbose      *bool                    `json:"verbose,omitempty"`
	Providers    []ProviderRecordConfig   `json:"providers,omitempty"`
	Truncation   *TruncationConfig        `json:"truncation,omitempty"`
	Budget       *BudgetConfig            `json:"budget,omitempty"`
	Goal         *GoalConfig              `json:"goal,omitempty"`
	Notify       *NotifyConfig            `json:"notify,omitempty"`
	Pricing      *PricingConfig           `json:"pricing,omitempty"`
	Trajectory   *TrajectoryConfig        `json:"trajectory,omitempty"`
	Agents       map[string]AgentOverride `json:"agents,omitempty"`
	Sandbox      *SandboxConfig           `json:"sandbox,omitempty"`
	Theme        string                   `json:"theme,omitempty"`
	SessionDir   string                   `json:"session_dir,omitempty"`
	// CheckpointDir is where the pre-images of tool writes are kept, so a write
	// can be undone. It is deliberately outside the project root: the fence that
	// confines every tool write then confines the store as well.
	CheckpointDir string     `json:"checkpoint_dir,omitempty"`
	LSP           *LSPConfig `json:"lsp,omitempty"`
	LogLevel      string     `json:"log_level,omitempty"`

	// Context window and compression
	ContextLength        int `json:"context_length,omitempty"`
	CompressionThreshold int `json:"compression_threshold,omitempty"`

	// Web search backends
	SearXNGURL string `json:"searxng_url,omitempty"`

	// MCP servers
	MCPServers []MCPServerConfig `json:"mcp_servers,omitempty"`
}

// TokenBudgets returns the configured cumulative token limits, or zero for each
// when no budget is configured. Zero is unlimited, and it is also what a caller
// passes on to the agent loop.
func (c Config) TokenBudgets() (perRun, perSession int) {
	if c.Budget == nil {
		return 0, 0
	}
	return c.Budget.MaxTokensPerRun, c.Budget.MaxTokensPerSession
}

// NotifyPolicy returns the notification channel and the shortest thing worth
// announcing. An absent section yields the built-in policy rather than silence:
// see internal/notify for why the default is on and terminal-only.
func (c Config) NotifyPolicy() (channel string, minDuration time.Duration) {
	if c.Notify == nil {
		return "", 0
	}
	return c.Notify.Channel, time.Duration(c.Notify.MinDurationSeconds) * time.Second
}

// DefaultConfig returns the hardcoded default configuration.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	showThinking := true
	return Config{
		DefaultMode:  "plan",
		ShowThinking: &showThinking,
		Providers: []ProviderRecordConfig{
			{
				// Issue #226: DeepSeek publishes `deepseek-flash`; the older
				// "deepseek-v4-flash" is a retired alias the vendor still accepts
				// and bills at this price. The model name is also the key a
				// declared rate is looked up by, so the default sends the name the
				// vendor documents.
				Name:    "deepseek",
				Type:    "openai",
				Model:   "deepseek-flash",
				BaseURL: "https://api.deepseek.com",
			},
		},
		Truncation: &TruncationConfig{
			MaxLines:  2000,
			MaxBytes:  200 * 1024,
			OutputDir: "/tmp/tinycode/truncated",
		},
		Agents: map[string]AgentOverride{
			"plan": {
				MaxSteps:    20,
				DeniedTools: []string{"write_file", "git_commit", "sandbox_allow", "task", "skill_manage"},
			},
			"explore": {
				// Read-only sub-agent: keep the built-in read_file/search_files
				// permission ruleset (see agent.DefaultAgents) instead of
				// granting bash here.
				MaxSteps: 15,
			},
		},
		Goal: &GoalConfig{
			MaxGoalRounds:    defaultMaxGoalRounds,
			IdleGraceSeconds: defaultIdleGraceSeconds,
			NoProgressRounds: defaultNoProgressRounds,
		},
		SessionDir:           filepath.Join(home, ".tinycode", "sessions"),
		CheckpointDir:        filepath.Join(home, ".tinycode", "checkpoints"),
		ContextLength:        1000000, // 1M on deepseek-flash (model version DeepSeek-V4.1-Flash)
		CompressionThreshold: 500000,  // 50% of context
	}
}

// readConfigFile is the file read every layer goes through. It is a package
// variable so a test can count how often a layer is opened; production always
// uses os.ReadFile.
var readConfigFile = os.ReadFile

// loadFile reads and parses a JSON config file, returning the partial config.
//
// The read and the decode are separate steps so a layer that needs more than the
// typed Config — the raw object, for the keys merge() cannot express — decodes
// the same bytes again instead of opening the file again (issue #231).
func loadFile(path string) (Config, error) {
	data, err := readConfigFile(path)
	if err != nil {
		return Config{}, err
	}

	cfg, err := decodeConfig(data)
	if err != nil {
		// A malformed config must not silently fall back to the defaults: say so
		// on stderr so the user can fix it.
		reportIgnoredConfig(path, err)
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// decodeConfig parses a document that has already been read. It neither reads
// nor reports, so a caller holding the bytes decides how often they are decoded
// and reports the outcome once.
//
// A key beginning with "$" is documentation rather than data — the "$comment"
// blocks config.example.json is written with — and is dropped before decoding, at
// any depth. That has to happen here rather than in each section: the sections
// that decode into a map (agents, pricing.prices, an MCP server's env and
// headers) are exactly the ones where a note would be read as an entry, and a
// single note in the wrong place used to make the whole file fail to load, which
// both callers report as "no file at all" (issue #229).
func decodeConfig(data []byte) (Config, error) {
	// Invalid JSON is left for the decode below to report instead, so a malformed
	// file still produces one message from one place rather than two.
	document, err := stripDocumentationKeys(data)
	if err != nil {
		document = data
	}

	var cfg Config
	if err := json.Unmarshal(document, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// reportIgnoredConfig is the one line a malformed file costs, wherever it was
// read from.
func reportIgnoredConfig(path string, err error) {
	fmt.Fprintf(os.Stderr, "tinycode: ignoring %s: %v\n", path, err)
}

// stripDocumentationKeys removes every object key whose name begins with "$",
// recursively, and returns the JSON that is left.
//
// Values travel as json.RawMessage, so their text is preserved rather than
// re-encoded: a number wider than a float64, and an escape such as
// "\u003cPROJECT_ROOT\u003e", both survive the walk unchanged.
//
// It returns an error only when data is not valid JSON at the top level; the
// caller reports that through the same path as any other parse failure.
func stripDocumentationKeys(data []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return data, nil
	}

	switch trimmed[0] {
	case '{':
		var object map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &object); err != nil {
			return nil, err
		}
		kept := make(map[string]json.RawMessage, len(object))
		for key, value := range object {
			if strings.HasPrefix(key, "$") {
				continue
			}
			cleaned, err := stripDocumentationKeys(value)
			if err != nil {
				return nil, err
			}
			kept[key] = cleaned
		}
		return json.Marshal(kept)
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, err
		}
		kept := make([]json.RawMessage, 0, len(items))
		for _, item := range items {
			cleaned, err := stripDocumentationKeys(item)
			if err != nil {
				return nil, err
			}
			kept = append(kept, cleaned)
		}
		return json.Marshal(kept)
	default:
		return trimmed, nil
	}
}

// merge applies overrides from src into dst (non-zero fields override).
func merge(dst, src Config) Config {
	if src.DefaultMode != "" {
		dst.DefaultMode = src.DefaultMode
	}
	if src.ShowThinking != nil {
		dst.ShowThinking = src.ShowThinking
	}
	if src.Verbose != nil {
		dst.Verbose = src.Verbose
	}
	if len(src.Providers) > 0 {
		dst.Providers = src.Providers
	}
	if src.Truncation != nil {
		if dst.Truncation == nil {
			dst.Truncation = &TruncationConfig{}
		}
		if src.Truncation.MaxLines > 0 {
			dst.Truncation.MaxLines = src.Truncation.MaxLines
		}
		if src.Truncation.MaxBytes > 0 {
			dst.Truncation.MaxBytes = src.Truncation.MaxBytes
		}
		if src.Truncation.OutputDir != "" {
			dst.Truncation.OutputDir = src.Truncation.OutputDir
		}
	}
	if src.Budget != nil {
		if dst.Budget == nil {
			dst.Budget = &BudgetConfig{}
		}
		// A negative value in a file is not a way to switch a limit off; only
		// zero (or absent) is unlimited, so a typo cannot widen a fence.
		if src.Budget.MaxTokensPerRun > 0 {
			dst.Budget.MaxTokensPerRun = src.Budget.MaxTokensPerRun
		}
		if src.Budget.MaxTokensPerSession > 0 {
			dst.Budget.MaxTokensPerSession = src.Budget.MaxTokensPerSession
		}
	}
	if src.Goal != nil {
		if dst.Goal == nil {
			dst.Goal = &GoalConfig{}
		}
		// Only positive values replace a default, for the same reason the token
		// budgets do: a zero or a typo is not a way to switch a guard off.
		if src.Goal.MaxGoalRounds > 0 {
			dst.Goal.MaxGoalRounds = src.Goal.MaxGoalRounds
		}
		if src.Goal.IdleGraceSeconds > 0 {
			dst.Goal.IdleGraceSeconds = src.Goal.IdleGraceSeconds
		}
		if src.Goal.NoProgressRounds > 0 {
			dst.Goal.NoProgressRounds = src.Goal.NoProgressRounds
		}
	}
	if src.Pricing != nil {
		if dst.Pricing == nil {
			dst.Pricing = &PricingConfig{}
		}
		if src.Pricing.Currency != "" {
			dst.Pricing.Currency = src.Pricing.Currency
		}
		// Rates merge per key, so a project file can price one route the user's
		// global file does not mention. A later layer wins for the keys it names
		// and leaves the rest alone.
		if len(src.Pricing.Prices) > 0 {
			if dst.Pricing.Prices == nil {
				dst.Pricing.Prices = map[string]PriceEntry{}
			}
			for key, entry := range src.Pricing.Prices {
				dst.Pricing.Prices[key] = entry
			}
		}
	}
	if src.Notify != nil {
		if dst.Notify == nil {
			dst.Notify = &NotifyConfig{}
		}
		if src.Notify.Channel != "" {
			dst.Notify.Channel = src.Notify.Channel
		}
		// A negative duration is not a way to switch the threshold off; only zero
		// is "announce everything".
		if src.Notify.MinDurationSeconds >= 0 {
			dst.Notify.MinDurationSeconds = src.Notify.MinDurationSeconds
		}
	}
	if src.Trajectory != nil {
		if dst.Trajectory == nil {
			dst.Trajectory = &TrajectoryConfig{}
		}
		// A pointer, so a layer can switch recording off and a later layer's
		// absent field does not switch it back on.
		if src.Trajectory.Enabled != nil {
			dst.Trajectory.Enabled = src.Trajectory.Enabled
		}
		if src.Trajectory.Dir != "" {
			dst.Trajectory.Dir = src.Trajectory.Dir
		}
		if src.Trajectory.Capture != "" {
			dst.Trajectory.Capture = src.Trajectory.Capture
		}
		if src.Trajectory.MaxRuns > 0 {
			dst.Trajectory.MaxRuns = src.Trajectory.MaxRuns
		}
		if src.Trajectory.MaxBytes > 0 {
			dst.Trajectory.MaxBytes = src.Trajectory.MaxBytes
		}
	}
	if src.SessionDir != "" {
		dst.SessionDir = src.SessionDir
	}
	if src.CheckpointDir != "" {
		dst.CheckpointDir = src.CheckpointDir
	}
	if src.LogLevel != "" {
		dst.LogLevel = src.LogLevel
	}
	if src.ContextLength > 0 {
		dst.ContextLength = src.ContextLength
	}
	if src.CompressionThreshold > 0 {
		dst.CompressionThreshold = src.CompressionThreshold
	}
	if src.LSP != nil {
		if dst.LSP == nil {
			dst.LSP = &LSPConfig{}
		}
		if src.LSP.Enabled {
			dst.LSP.Enabled = true
		}
	}
	if src.Theme != "" {
		dst.Theme = src.Theme
	}
	if src.SearXNGURL != "" {
		dst.SearXNGURL = src.SearXNGURL
	}
	if len(src.MCPServers) > 0 {
		dst.MCPServers = src.MCPServers
	}

	// Merge sandbox hardening settings field by field so a project config can
	// add deny rules without dropping the user-level ones.
	if src.Sandbox != nil {
		if dst.Sandbox == nil {
			dst.Sandbox = &SandboxConfig{}
		}
		if src.Sandbox.ProjectRoot != "" {
			dst.Sandbox.ProjectRoot = src.Sandbox.ProjectRoot
		}
		if len(src.Sandbox.DenyCommands) > 0 {
			dst.Sandbox.DenyCommands = append(dst.Sandbox.DenyCommands, src.Sandbox.DenyCommands...)
		}
		if len(src.Sandbox.AllowedPaths) > 0 {
			dst.Sandbox.AllowedPaths = append(dst.Sandbox.AllowedPaths, src.Sandbox.AllowedPaths...)
		}
		// A bool has no "unset" to distinguish from false, so it is only
		// inherited when the overlay turns it on: a project file cannot turn
		// off a user-level requirement, and a user file cannot turn it on by
		// being absent.
		if src.Sandbox.RequireHardBoundary {
			dst.Sandbox.RequireHardBoundary = true
		}
		// Confinement can be turned on by any layer (narrowing); turning it
		// off is a widening, so only the user's own file may do it and that is
		// resolved from the user layer by UserConfineCommands rather than here
		// — merge() cannot tell which layer it is merging.
		if src.Sandbox.ConfineCommands != nil && *src.Sandbox.ConfineCommands {
			on := true
			dst.Sandbox.ConfineCommands = &on
		}
	}

	// Merge agent overrides
	if dst.Agents == nil {
		dst.Agents = make(map[string]AgentOverride)
	}
	for name, override := range src.Agents {
		existing, has := dst.Agents[name]
		if !has {
			dst.Agents[name] = override
			continue
		}
		if override.MaxSteps > 0 {
			existing.MaxSteps = override.MaxSteps
		}
		if override.SystemPrompt != "" {
			existing.SystemPrompt = override.SystemPrompt
		}
		if override.Model != "" {
			existing.Model = override.Model
		}
		if override.Permissions != nil {
			existing.Permissions = override.Permissions
		}
		if override.AllowedTools != nil {
			existing.AllowedTools = override.AllowedTools
		}
		if override.DeniedTools != nil {
			existing.DeniedTools = override.DeniedTools
		}
		dst.Agents[name] = existing
	}

	return dst
}

// UserLayer is the user-global config file, read once.
//
// Every consumer in one process start takes this value instead of opening the
// file again: one read per reader reported a malformed file once per reader
// (issue #231) and let two readers of one start disagree when the file changed in
// between. Startup builds this once and passes it down; the standalone loaders
// below read the file afresh for the paths that write it and reload.
//
// The raw object travels with the decoded Config because merge() cannot express
// every key the file carries: an explicit `confine_commands: false` survives no
// merged Config (issue #162).
type UserLayer struct {
	cfg    Config
	raw    map[string]any
	rawErr error
}

// LoadUserLayer reads ~/.tinycode/config.json once.
//
// A missing file is the defaults and no diagnostic; a malformed one is reported
// exactly once, here, and the defaults are used rather than a partial parse.
func LoadUserLayer() UserLayer {
	home, err := os.UserHomeDir()
	if err != nil {
		return UserLayer{cfg: DefaultConfig(), raw: map[string]any{}}
	}
	path := filepath.Join(home, ".tinycode", "config.json")

	data, err := readConfigFile(path)
	if err != nil {
		layer := UserLayer{cfg: DefaultConfig(), raw: map[string]any{}}
		if !os.IsNotExist(err) {
			// A missing file is normal; an unreadable one is not, and the raw
			// half keeps the error for the callers that report it (the grant
			// loader logs it, --list-grants turns it into an exit code).
			layer.rawErr = err
		}
		return layer
	}

	cfg := DefaultConfig()
	if userCfg, err := decodeConfig(data); err == nil {
		cfg = merge(cfg, userCfg)
	} else {
		reportIgnoredConfig(path, err)
	}

	// The raw form comes from the same bytes. Its own failure is not reported a
	// second time — reportIgnoredConfig above already named the file — but it is
	// kept, because the write paths fail loudly on a file they cannot parse.
	raw := map[string]any{}
	rawErr := error(nil)
	if err := json.Unmarshal(data, &raw); err != nil {
		raw, rawErr = map[string]any{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return UserLayer{cfg: cfg, raw: raw, rawErr: rawErr}
}

// Config is the code defaults merged with the user file.
func (l UserLayer) Config() Config { return l.cfg }

// SandboxBoundary is this layer's view of the two keys that can widen the fence.
func (l UserLayer) SandboxBoundary() SandboxBoundary { return sandboxBoundaryOf(l.cfg) }

// ConfineCommands is the raw confine_commands value and whether the file set it.
//
// It reads the raw object rather than the merged Config because merge() keeps
// only the narrowing direction of this key: an explicit false survives no merged
// Config, so the layer is the only place it can be read.
func (l UserLayer) ConfineCommands() (value bool, set bool) {
	sandbox, _ := l.raw["sandbox"].(map[string]any)
	if sandbox == nil {
		return false, false
	}
	value, ok := sandbox["confine_commands"].(bool)
	return value, ok
}

// ResolveTrajectoryCapture decides the capture mode from the layers that may
// speak to it.
//
// `capture: "full"` writes tool arguments and results to disk, which is a
// privacy decision, so it is honoured only when the **user's own** file asks for
// it. A project-local `.tinycode/config.json` that turns it on is ignored, and
// the ignored key is reported the same way the sandbox reports a boundary key a
// cloned repository tried to widen: a person who wrote it can see it did not
// take effect. Recording itself is not affected — a project may switch it off,
// and off is not a widening.
// The second result reports that the request was ignored, so the composition can
// say so: a config layer is not the place to decide that a log line is wanted.
func (l UserLayer) ResolveTrajectoryCapture(cfg *Config) (capture string, ignored bool) {
	if cfg.TrajectorySpec().Capture != TrajectoryCaptureFull {
		return TrajectoryCaptureMetadata, false
	}
	if !l.wantsFullCapture() {
		return TrajectoryCaptureMetadata, true
	}
	return TrajectoryCaptureFull, false
}

// wantsFullCapture reports whether this layer's own file asks for full capture.
func (l UserLayer) wantsFullCapture() bool {
	section, _ := l.raw["trajectory"].(map[string]any)
	if section == nil {
		return false
	}
	capture, ok := section["capture"].(string)
	return ok && strings.EqualFold(capture, TrajectoryCaptureFull)
}

// AllowedPathGrants is the grants this layer carries, with the raw file's own
// read or decode failure so a caller can keep reporting it.
func (l UserLayer) AllowedPathGrants() ([]AllowedPathGrant, error) {
	if l.rawErr != nil {
		return nil, l.rawErr
	}
	return grantsFromRaw(l.raw), nil
}

// ProjectLayer is the project-local ./.tinycode/config.json, read once for the
// same reason as UserLayer. A parse failure is reported by loadFile and leaves
// the layer empty, which is what the merged loader does with it.
type ProjectLayer struct {
	cfg Config
}

// LoadProjectLayer reads the project-local file once.
func LoadProjectLayer() ProjectLayer {
	cfg, err := loadFile(filepath.Join(".tinycode", "config.json"))
	if err != nil {
		return ProjectLayer{}
	}
	return ProjectLayer{cfg: cfg}
}

// Config is the project-local file as written, or the zero Config when it is
// missing or malformed.
func (l ProjectLayer) Config() Config { return l.cfg }

// SandboxBoundary is this layer's view of the widening keys, so a caller can
// report what it ignored rather than dropping it in silence.
func (l ProjectLayer) SandboxBoundary() SandboxBoundary { return sandboxBoundaryOf(l.cfg) }

// Layers is one process start's read of both configuration files.
//
// Startup reads this once and passes it down, so every reader of a layer takes
// the value instead of opening the file again (issue #231).
type Layers struct {
	User    UserLayer
	Project ProjectLayer
}

// LoadLayers reads the user and project layers, one open each.
func LoadLayers() Layers {
	return Layers{User: LoadUserLayer(), Project: LoadProjectLayer()}
}

// Config is the merged configuration: code defaults, then the user file, then
// the project-local file.
func (l Layers) Config() Config {
	return merge(l.User.Config(), l.Project.Config())
}

// LoadUserConfig loads only the code defaults and the user-global config file,
// skipping the project-local layer. Use it when persisting settings back to
// disk: a repository's ./.tinycode/config.json is attacker-controlled, so its
// providers or system prompts must never be promoted into the user's global
// config by an unrelated action such as "Always allow".
//
// It reads the file for this call. Startup wiring builds a UserLayer once
// instead, so a malformed file is not reported once per reader (issue #231).
func LoadUserConfig() Config {
	return LoadUserLayer().Config()
}

// LoadConfig loads the configuration from all sources and returns the merged result.
// Load order: defaults → ~/.tinycode/config.json → ./.tinycode/config.json
func LoadConfig() Config {
	return LoadLayers().Config()
}

// SandboxBoundary is the part of the sandbox configuration that can widen the
// fence: the directory the boundary is built around, and the paths allowed
// beyond it.
type SandboxBoundary struct {
	ProjectRoot  string
	AllowedPaths []string
}

// UserSandboxBoundary returns those keys from the code defaults and the user's
// own ~/.tinycode/config.json.
//
// The project-local ./.tinycode/config.json is deliberately not consulted. It is
// checked out with the repository, so it is attacker-controlled: a repository
// carrying {"sandbox":{"project_root":"/"}} would build the fence around the
// whole filesystem, and one carrying allowed_paths would hand the agent a path
// the person running it never allowed (issue #162).
//
// The keys that can only narrow behaviour are a different question, and merge()
// already treats them that way: deny rules accumulate, and the two hardening
// booleans can be turned on by any layer but never off.
//
// It reads the file for this call; the startup path takes the UserLayer it
// already read instead (see UserLayer.SandboxBoundary).
func UserSandboxBoundary() SandboxBoundary {
	return LoadUserLayer().SandboxBoundary()
}

// ProjectSandboxBoundary returns the same keys as the project-local file spells
// them, so a caller can report what it ignored rather than dropping it in
// silence. It reads the file for this call; the startup path takes the
// ProjectLayer it already read instead.
func ProjectSandboxBoundary() SandboxBoundary {
	return LoadProjectLayer().SandboxBoundary()
}

func sandboxBoundaryOf(cfg Config) SandboxBoundary {
	if cfg.Sandbox == nil {
		return SandboxBoundary{}
	}
	return SandboxBoundary{
		ProjectRoot:  cfg.Sandbox.ProjectRoot,
		AllowedPaths: cfg.Sandbox.AllowedPaths,
	}
}

// UserConfineCommands returns the confine_commands value from the user's own
// ~/.tinycode/config.json and whether it was present. The project-local file is
// not consulted: turning confinement off widens the fence, and a repository
// that arrives with the checkout must not be able to do that (issue #162).
//
// It reads the raw user file rather than the merged one because merge() keeps
// only the narrowing direction of this key: an explicit false survives no
// merged Config, so the layer is the only place it can be read. It reads the
// file for this call; the startup path asks the UserLayer it already read
// instead (see UserLayer.ConfineCommands).
func UserConfineCommands() (value bool, set bool) {
	return LoadUserLayer().ConfineCommands()
}

// sandboxAsksForConfinement reports whether a merged configuration carries an
// explicit request to confine commands. Past the user layer such a request can
// only have come from the project-local file, because merge() keeps this key's
// "on" direction and drops its "off" one.
func sandboxAsksForConfinement(cfg *Config) bool {
	return cfg != nil && cfg.Sandbox != nil && cfg.Sandbox.ConfineCommands != nil && *cfg.Sandbox.ConfineCommands
}

// ResolveConfineCommands decides whether shell commands run under the kernel
// boundary, given whether this host can confine a subprocess.
//
// The user's own configuration decides when it names the key. Both directions
// are the user's to choose: off where the platform default is on is the
// documented escape hatch for a workflow that needs the wider boundary, and on
// where the default is off is an explicit request — on a host with no mechanism
// the launcher then refuses every command, which is the honest answer to a
// request a person made in their own file, and /sandbox states it as such.
//
// With no user-level value the answer is the platform default: on where the
// host can apply the boundary, off where it cannot. A project-local on-request
// cannot move that answer, and the no-mechanism half is why — merge() keeps
// "on" from any layer, so without the veto a repository shipping
// `confine_commands: true` would turn confinement on for a host that cannot
// apply it and refuse every command in a session that merely cloned it (macOS).
// Narrowing is still allowed where it can actually be applied, which is what
// the platform default already gives.
//
// It reads the user file for this call; the startup path passes the UserLayer it
// already read instead (see UserLayer.ResolveConfineCommands).
func ResolveConfineCommands(cfg *Config, hostHasMechanism bool) bool {
	return LoadUserLayer().ResolveConfineCommands(cfg, hostHasMechanism)
}

// ResolveConfineCommands is the same decision with the user's answer taken from
// this layer, so one start reads the file once however many readers ask.
func (l UserLayer) ResolveConfineCommands(cfg *Config, hostHasMechanism bool) bool {
	if value, set := l.ConfineCommands(); set {
		return value
	}
	// Vetoed: the request came from the project-local file and this host cannot
	// honour it, so the platform default (off) stands rather than a session in
	// which every command is refused.
	if sandboxAsksForConfinement(cfg) && !hostHasMechanism {
		return false
	}
	return hostHasMechanism
}

// Save persists the configuration to the user's global config file.
func (cfg Config) Save() error {
	path, err := userConfigPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// AddAllowedPath appends path to sandbox.allowed_paths in the *user* config
// file, preserving every other key in that file (including keys this build does
// not know about). It deliberately does not re-serialise a merged Config, which
// would freeze today's defaults into the user's file and could leak project-local
// settings.
func AddAllowedPath(path string) error {
	if path == "" {
		return fmt.Errorf("refusing to allow an empty path")
	}
	file, err := userConfigPath()
	if err != nil {
		return err
	}

	// Start from the raw file so unknown keys survive the round trip. A missing
	// or malformed file starts from an empty object.
	raw := map[string]any{}
	if data, readErr := os.ReadFile(file); readErr == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse %s: %w", file, err)
		}
	}

	sandbox, _ := raw["sandbox"].(map[string]any)
	if sandbox == nil {
		sandbox = map[string]any{}
	}
	existing, _ := sandbox["allowed_paths"].([]any)
	for _, p := range existing {
		if s, ok := p.(string); ok && s == path {
			return nil // already allowed
		}
	}
	sandbox["allowed_paths"] = append(existing, path)
	raw["sandbox"] = sandbox

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(file, data)
}

// userConfigPath returns ~/.tinycode/config.json, creating the directory.
func userConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".tinycode", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	return path, nil
}

// UserConfigPath is the file a persistent grant is written to, for callers that
// must name it: a person choosing "always allow" is changing a file, and the
// choice is only deliberate if the file is visible when it is made.
func UserConfigPath() (string, error) { return userConfigPath() }

// AllowedPathGrant is one persistent permission grant: a path a person allowed
// beyond the current session, with the context needed to audit it later.
type AllowedPathGrant struct {
	Path string `json:"path"`
	// Granted is when the grant was recorded. It is zero for a grant read from
	// the older `allowed_paths` list, which carried no context.
	Granted time.Time `json:"granted,omitempty"`
	// Project is the project root in effect when the grant was made, so a
	// grant that no longer fits where it came from can be spotted.
	Project string `json:"project,omitempty"`
	// Legacy marks a grant that predates the record format.
	Legacy bool `json:"-"`
}

// ListAllowedPathGrants returns every persistent grant, newest format and
// legacy list alike, sorted by path. A path present in both forms appears once,
// described by its record.
func ListAllowedPathGrants() ([]AllowedPathGrant, error) {
	file, err := userConfigPath()
	if err != nil {
		return nil, err
	}
	raw, err := readConfigObject(file)
	if err != nil {
		return nil, err
	}
	return grantsFromRaw(raw), nil
}

// AddAllowedPathGrant records a persistent grant for path, preserving every
// other key in the user's file (including keys this build does not know about).
// A path already granted in either form is left alone rather than duplicated,
// so the recorded time and project describe when the grant was actually made.
func AddAllowedPathGrant(path, project string) error {
	if path == "" {
		return fmt.Errorf("refusing to allow an empty path")
	}
	file, err := userConfigPath()
	if err != nil {
		return err
	}
	raw, err := readConfigObject(file)
	if err != nil {
		return err
	}
	for _, grant := range grantsFromRaw(raw) {
		if grant.Path == path {
			return nil // already granted
		}
	}

	sandbox := sandboxObject(raw)
	records, _ := sandbox["allowed_path_grants"].([]any)
	sandbox["allowed_path_grants"] = append(records, map[string]any{
		"path":    path,
		"granted": time.Now().UTC().Format(time.RFC3339),
		"project": project,
	})
	raw["sandbox"] = sandbox

	return writeConfigObject(file, raw)
}

// RevokeAllowedPathGrant removes path from both the record list and the legacy
// list, and reports whether anything was removed. Revoking is the only way a
// persistent grant ends, so it has to be able to reach every form it can be
// stored in.
func RevokeAllowedPathGrant(path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("refusing to revoke an empty path")
	}
	file, err := userConfigPath()
	if err != nil {
		return false, err
	}
	raw, err := readConfigObject(file)
	if err != nil {
		return false, err
	}
	sandbox := sandboxObject(raw)

	removed := false
	if records, ok := sandbox["allowed_path_grants"].([]any); ok {
		kept := make([]any, 0, len(records))
		for _, record := range records {
			if m, ok := record.(map[string]any); ok && m["path"] == path {
				removed = true
				continue
			}
			kept = append(kept, record)
		}
		if len(kept) == 0 {
			delete(sandbox, "allowed_path_grants")
		} else {
			sandbox["allowed_path_grants"] = kept
		}
	}
	if legacy, ok := sandbox["allowed_paths"].([]any); ok {
		kept := make([]any, 0, len(legacy))
		for _, entry := range legacy {
			if s, ok := entry.(string); ok && s == path {
				removed = true
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == 0 {
			delete(sandbox, "allowed_paths")
		} else {
			sandbox["allowed_paths"] = kept
		}
	}
	if !removed {
		return false, nil
	}
	raw["sandbox"] = sandbox
	if err := writeConfigObject(file, raw); err != nil {
		return false, err
	}
	return true, nil
}

// readConfigObject reads the raw user config as a generic object so unknown
// keys survive a round trip. A missing file is an empty object; a malformed one
// is an error rather than a silent overwrite.
func readConfigObject(file string) (map[string]any, error) {
	raw := map[string]any{}
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return raw, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	return raw, nil
}

func writeConfigObject(file string, raw map[string]any) error {
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(file, data)
}

func sandboxObject(raw map[string]any) map[string]any {
	if sandbox, ok := raw["sandbox"].(map[string]any); ok {
		return sandbox
	}
	return map[string]any{}
}

// grantsFromRaw reads both storage forms. Records win over the legacy list for
// the same path, because a record is what a grant looks like once it has
// context.
func grantsFromRaw(raw map[string]any) []AllowedPathGrant {
	sandbox := sandboxObject(raw)
	if len(sandbox) == 0 {
		return nil
	}

	byPath := map[string]AllowedPathGrant{}
	if legacy, ok := sandbox["allowed_paths"].([]any); ok {
		for _, entry := range legacy {
			if s, ok := entry.(string); ok && s != "" {
				byPath[s] = AllowedPathGrant{Path: s, Legacy: true}
			}
		}
	}
	if records, ok := sandbox["allowed_path_grants"].([]any); ok {
		for _, record := range records {
			m, ok := record.(map[string]any)
			if !ok {
				continue
			}
			path, _ := m["path"].(string)
			if path == "" {
				continue
			}
			grant := AllowedPathGrant{Path: path}
			grant.Project, _ = m["project"].(string)
			if s, ok := m["granted"].(string); ok {
				if when, err := time.Parse(time.RFC3339, s); err == nil {
					grant.Granted = when
				}
			}
			byPath[path] = grant
		}
	}

	grants := make([]AllowedPathGrant, 0, len(byPath))
	for _, grant := range byPath {
		grants = append(grants, grant)
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].Path < grants[j].Path })
	return grants
}

// writeFileAtomic writes data to path through a temp file in the same directory
// so a crash mid-write cannot truncate the file. The file is created 0600
// because it can contain API endpoints and other local preferences.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
