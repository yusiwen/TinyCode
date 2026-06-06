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
	Streaming        bool // true while still receiving deltas
	Rendered         string // glamour-rendered content (set on StreamDone, deprecated)
	Blocks           []ContentBlock // structured content (new, replaces Rendered)
}

// TuiStatus indicates the current TUI state.
type TuiStatus int

const (
	StatusIdle TuiStatus = iota
	StatusStreaming
	StatusError
)
