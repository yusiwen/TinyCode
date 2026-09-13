package tui

import "github.com/yusiwen/tinycode/tool"

// StreamMsg is sent from the agent goroutine to the TUI for each streaming delta.
type StreamMsg struct {
	RunID          uint64 // generation id of the producing run (0 in legacy tests)
	ReasoningDelta string
	TextDelta      string
}

// StreamDone is sent when the agent completes (final answer or error).
type StreamDone struct {
	RunID            uint64 // generation id of the producing run (0 in legacy tests)
	Content          string
	ReasoningContent string
	Error            error
	IsIntermediate   bool // true: more steps to follow, false: final response
}

// ChatMsg is sent when the user submits input.
type ChatMsg struct {
	Text string
}

// modeSwitchMsg is sent when the user presses Tab to switch modes.
type modeSwitchMsg struct{}

// chatMessage holds one message in the conversation view.
type chatMessage struct {
	Role             string // "user", "assistant"
	Content          string
	ReasoningContent string
	ReasoningFolded  bool
	ToolCalls        []ToolCallInfo // in-order tool calls during this message
	Streaming        bool
	Blocks           []ContentBlock
	TodoSnapshot     []tool.TodoItem // snapshot taken at StreamDone (for per-message TODO display)
}

// ToolCallInfo records one tool invocation during a message.
type ToolCallInfo struct {
	Name string
	Arg  string // short summary, e.g. filename or key argument
}

// ToolCallMsg is sent when the agent invokes a tool.
type ToolCallMsg struct {
	RunID  uint64 // generation id of the producing run (0 in legacy tests)
	MsgIdx int    // assistant message index
	Name   string
	Arg    string
}

// ToolResultMsg is sent when the tool returns (used to track duration).
type ToolResultMsg struct {
	RunID  uint64
	MsgIdx int
	Name   string        // tool name, so TUI can react (e.g. mark todoDirty)
	AckCh  chan struct{} // non-nil for "todo" — agent blocks until render confirmed
}

// sessionTitleMsg carries the result of asynchronous session-title generation.
type sessionTitleMsg struct {
	Title string
}

// LSPDiagMsg is sent when LSP diagnostics are available.
type LSPDiagMsg struct {
	FilePath string
	Count    int
}

// TuiStatus indicates the current TUI state.
type TuiStatus int

const (
	StatusIdle TuiStatus = iota
	StatusStreaming
	StatusError
)
