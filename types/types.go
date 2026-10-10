package types

import "context"

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
}

// Usage is the token accounting a provider reported for one request.
//
// It travels as a pointer on ChatResponse because "this endpoint reported no
// usage" and "it reported zero tokens" are different facts: the first has to
// fall back to an estimate, the second must not.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Add returns the sum of two usage records, so a caller can accumulate a
// session total without adding the fields itself.
func (u Usage) Add(v Usage) Usage {
	return Usage{
		PromptTokens:     u.PromptTokens + v.PromptTokens,
		CompletionTokens: u.CompletionTokens + v.CompletionTokens,
		TotalTokens:      u.TotalTokens + v.TotalTokens,
	}
}

// ChatResponse is the LLM's reply — either text or tool calls.
type ChatResponse struct {
	Content          string
	ToolCalls        []ToolCall
	ReasoningContent string // DeepSeek thinking mode
	Usage            *Usage // provider-reported token usage; nil when the endpoint reported none
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
