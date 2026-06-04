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

// ChatRequest bundles a full LLM call.
type ChatRequest struct {
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
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
