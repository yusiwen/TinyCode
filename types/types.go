package types

import (
	"context"
	"sort"
	"strconv"
	"strings"
)

// Role constants for messages.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message represents a single message in the conversation.
type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	Name             string     `json:"name,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"` // DeepSeek thinking mode
}

// Memory represents a remembered fact.
type Memory struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// MemoryStore is the abstraction for long-term knowledge.
type MemoryStore interface {
	Remember(key, value string) error
	Recall(query string, limit int) ([]Memory, error)
	Forget(key string) error
	List() ([]Memory, error)
}

// ChatRequest holds parameters for an LLM chat call.
type ChatRequest struct {
	Messages        []Message
	Tools           []ToolDef
	MaxTokens       int
	Model           string           // optional: override provider's default model
	StreamCallbacks *StreamCallbacks // optional SSE callbacks for real-time display
}

// StreamCallbacks provides real-time streaming callbacks for SSE responses.
type StreamCallbacks struct {
	OnReasoningDelta func(text string)
	OnTextDelta      func(text string)
	OnToolCall       func(name string, arg string) // called before each tool execution
	OnToolResult     func(name string)             // called after each tool result
	OnStepDone       func()                        // called after all tools complete for one step
	OnUsage          func(usage Usage)             // called once per LLM call that reported token usage
	OnCost           func(event CostEvent)         // called once per LLM call, including one whose cost is unknown
}

// Usage is the token accounting a provider reported for one request.
//
// It travels as a pointer on ChatResponse because "this endpoint reported no
// usage" and "it reported zero tokens" are different facts: the first has to
// fall back to an estimate, the second must not.
//
// The fields are one per *billing lane*, not one per wire name. A route that
// reports prompt_tokens_details.cached_tokens and one that reports DeepSeek's
// prompt_cache_hit_tokens mean the same thing, and both land in
// CachedPromptTokens, so nothing downstream has to know which route it is on.
// The detail fields are part of the counts beside them, not extra tokens:
// CachedPromptTokens and ReasoningTokens are subsets of PromptTokens and
// CompletionTokens respectively.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	// CachedPromptTokens is the part of PromptTokens the provider served from a
	// cache, billed below the standard input rate.
	CachedPromptTokens int `json:"cached_prompt_tokens,omitempty"`
	// CacheWriteTokens is the part written *into* an explicit cache, billed
	// above the standard input rate by the providers that charge for it.
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	// ReasoningTokens is the part of CompletionTokens spent on reasoning, which
	// some providers price separately from the answer.
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// Add returns the sum of two usage records, so a caller can accumulate a
// session total without adding the fields itself.
func (u Usage) Add(v Usage) Usage {
	return Usage{
		PromptTokens:       u.PromptTokens + v.PromptTokens,
		CompletionTokens:   u.CompletionTokens + v.CompletionTokens,
		TotalTokens:        u.TotalTokens + v.TotalTokens,
		CachedPromptTokens: u.CachedPromptTokens + v.CachedPromptTokens,
		CacheWriteTokens:   u.CacheWriteTokens + v.CacheWriteTokens,
		ReasoningTokens:    u.ReasoningTokens + v.ReasoningTokens,
	}
}

// Cost is money, with the unit it is denominated in.
//
// The unit is not decoration: a provider may bill in its own credits while a
// user-declared price is in the currency of their invoice, and adding those
// together would produce a number with no meaning. Callers accumulate per
// currency rather than across it.
type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// CostSource says where a cost number came from. It is the same distinction the
// usage path draws between a reported record and an estimate, and it exists so
// that "we do not know what this call cost" can never be displayed as zero.
type CostSource string

const (
	// CostUnknown means no reported cost and no declared price: the call's cost
	// is not known, which is not the same as its being free.
	CostUnknown CostSource = "unknown"
	// CostReported means the provider said what it charged.
	CostReported CostSource = "reported"
	// CostDeclared means it was computed from a price the user declared; it is
	// an estimate of the bill, not a reading of it.
	CostDeclared CostSource = "declared"
)

// CostEvent is what one provider call cost, and where that number came from.
type CostEvent struct {
	Cost   Cost       `json:"cost"`
	Source CostSource `json:"source"`
}

// CostTotals accumulates money per unit.
//
// The unit is part of the key because two routes can bill differently — an
// account's own credits beside the currency of the user's invoice — and summing
// those into one number would produce something no one can act on. An empty
// currency key collects amounts whose route did not name a unit.
type CostTotals map[string]float64

// Add returns the totals with one cost added. It returns a new map rather than
// mutating, so a caller can accumulate onto a struct field in one expression.
func (t CostTotals) Add(c Cost) CostTotals {
	out := make(CostTotals, len(t)+1)
	for k, v := range t {
		out[k] = v
	}
	out[c.Currency] += c.Amount
	return out
}

// String renders the totals for display, ordered by unit so a caller can assert
// on the text. The order is case-insensitive — "credits" before "USD" reads the
// way a person would write it — with the raw string as a tiebreak so two units
// differing only in case still have a fixed order. An amount whose unit is
// unknown prints as a bare number: naming a unit the route did not give would be
// an invention.
func (t CostTotals) String() string {
	if len(t) == 0 {
		return ""
	}
	units := make([]string, 0, len(t))
	for k := range t {
		units = append(units, k)
	}
	sort.Slice(units, func(i, j int) bool {
		li, lj := strings.ToLower(units[i]), strings.ToLower(units[j])
		if li != lj {
			return li < lj
		}
		return units[i] < units[j]
	})
	parts := make([]string, 0, len(units))
	for _, unit := range units {
		amount := strconv.FormatFloat(t[unit], 'f', -1, 64)
		if unit == "" {
			parts = append(parts, amount)
			continue
		}
		parts = append(parts, amount+" "+unit)
	}
	return strings.Join(parts, ", ")
}

// ChatResponse is the LLM's reply — either text or tool calls.
type ChatResponse struct {
	Content          string
	ToolCalls        []ToolCall
	ReasoningContent string // DeepSeek thinking mode
	Usage            *Usage // provider-reported token usage; nil when the endpoint reported none
	Cost             *Cost  // provider-reported charge; nil when the endpoint reported none
}

// ToolDef describes one tool to the LLM (function calling schema).
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolCall is returned when the LLM decides to invoke a tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON
}

// SandboxMode names the file effects a run may perform. It is the same
// vocabulary the confinement boundary uses, so a policy value can be handed to
// it unchanged.
type SandboxMode string

const (
	// SandboxReadOnly denies writes except required sinks.
	SandboxReadOnly SandboxMode = "read-only"
	// SandboxWorkspaceWrite allows writes under the policy's roots.
	SandboxWorkspaceWrite SandboxMode = "workspace-write"
	// SandboxFullAccess applies no boundary.
	SandboxFullAccess SandboxMode = "danger-full-access"
)

// SandboxPolicy is one run's file-effect policy, decided once when the run
// starts and carried on its context.
//
// It is a value, not a reference to configuration: a run that has begun cannot
// have its mode or its writable roots changed by another run, by a later
// configuration edit, or by a grant given to a different call. Consumers
// (the path fence, the command boundary, the plan-mode guard) read it and
// nothing else; none of them re-derives a mode or a root.
type SandboxPolicy struct {
	Mode SandboxMode
	// ProjectRoot is the one root the single-root kernel probe can express
	// (openat2 RESOLVE_BENEATH). It is empty when no root is configured.
	ProjectRoot string
	// Roots is every directory a write may land under, canonical and
	// deduplicated. ProjectRoot is normally its first entry; other entries are
	// paths the session auto-allows, which the probe cannot cover.
	Roots []string
	// Source names where the policy came from, for reports. It is not a
	// security decision, only a way to explain one.
	Source string
}

// sandboxPolicyKey is the context key for the run's sandbox policy.
type sandboxPolicyKey struct{}

// WithSandboxPolicy returns a context carrying policy for the run that is about
// to start.
//
// It deliberately replaces any policy already there: a nested call that wants a
// wider boundary must say so explicitly with its own value, and that value
// lives no longer than the context it was attached to.
func WithSandboxPolicy(ctx context.Context, policy SandboxPolicy) context.Context {
	return context.WithValue(ctx, sandboxPolicyKey{}, policy)
}

// SandboxPolicyFrom returns the policy carried by ctx. The second result is
// false when the context has none, which is the case for a direct Agent use
// outside this binary's wiring; callers must then fall back to their own
// configuration rather than inventing a boundary.
func SandboxPolicyFrom(ctx context.Context) (SandboxPolicy, bool) {
	policy, ok := ctx.Value(sandboxPolicyKey{}).(SandboxPolicy)
	return policy, ok
}

// ResolveSandboxRoots is installed by the composition that owns the sandbox
// configuration, and answers one question: which roots are writable for this
// mode. It is a function rather than state — it returns a value that the caller
// then freezes into a SandboxPolicy, so a run's roots cannot move under it.
//
// It is a variable because the configuration lives in a package that cannot be
// imported here (the dependency runs the other way), and because tests need to
// present a configuration of their own. When it is nil, a run gets a policy
// with no writable roots.
var ResolveSandboxRoots = func(mode SandboxMode) (projectRoot string, roots []string) {
	return "", nil
}
