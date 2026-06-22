package types

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
	Messages []Message
	Tools    []ToolDef
	MaxTokens int
	StreamCallbacks *StreamCallbacks // optional SSE callbacks for real-time display
}

// StreamCallbacks provides real-time streaming callbacks for SSE responses.
type StreamCallbacks struct {
	OnReasoningDelta func(text string)
	OnTextDelta      func(text string)
	OnToolCall       func(name string, arg string) // called before each tool execution
	OnToolResult     func(name string)             // called after each tool result
	OnStepDone       func()                        // called after all tools complete for one step
}

// ChatResponse is the LLM's reply — either text or tool calls.
type ChatResponse struct {
	Content          string
	ToolCalls        []ToolCall
	ReasoningContent string // DeepSeek thinking mode
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

// PlanModeWriteRestricted controls whether bash blocks write operations.
// Set by agent when entering plan mode; checked by bash tool.
var PlanModeWriteRestricted bool
