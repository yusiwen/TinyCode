package tui

// StreamMsg is sent from the agent goroutine to the TUI for each streaming delta.
type StreamMsg struct {
	ReasoningDelta string
	TextDelta      string
}

// StreamDone is sent when the agent completes (final answer or error).
type StreamDone struct {
	Content          string
	ReasoningContent string
	Error            error
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
}

// ToolCallInfo records one tool invocation during a message.
type ToolCallInfo struct {
	Name string
	Arg  string // short summary, e.g. filename or key argument
}

// ToolCallMsg is sent when the agent invokes a tool.
type ToolCallMsg struct {
	MsgIdx int    // assistant message index
	Name   string
	Arg    string
}

// ToolResultMsg is sent when the tool returns (used to track duration).
type ToolResultMsg struct {
	MsgIdx int
	Name   string        // tool name, so TUI can react (e.g. mark todoDirty)
	AckCh  chan struct{} // non-nil for "todo" — agent blocks until render confirmed
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
