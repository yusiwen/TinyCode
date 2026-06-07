package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// scrollTestModel creates a model with a small viewport and many messages.
func scrollTestModel(msgCount int) *TuiModel {
	m := &TuiModel{
		ready:  true,
		width:  80,
		height: 20,
		selectStart: -1,
		selectEnd:   -1,
		status:  StatusIdle,
	}
	m.vp = viewport.New(80, 5) // tiny viewport: only 5 visible lines
	for i := 0; i < msgCount; i++ {
		role := "system"
		if i%3 == 0 {
			role = "user"
		}
		m.messages = append(m.messages, chatMessage{Role: role, Content: "line"})
	}
	return m
}

// renderView simulates View() by building msgLines, wrapping, and SetContent.
// Returns the model after View-side operations.
func renderView(m *TuiModel) *TuiModel {
	lines := buildMsgLines(m)
	var wrapped []string
	for _, line := range lines {
		wrapped = append(wrapped, wrapLine(line, m.vp.Width)...)
	}
	wasAtBottom := m.vp.AtBottom()
	m.vp.SetContent(strings.Join(wrapped, "\n"))
	if wasAtBottom {
		m.vp.GotoBottom()
	}
	return m
}

// buildMsgLines duplicates the msgLines construction logic from View().
func buildMsgLines(m *TuiModel) []string {
	var msgLines []string
	for _, msg := range m.messages {
		switch msg.Role {
		case "user":
			msgLines = append(msgLines, "> "+msg.Content)
		case "assistant":
			if msg.ReasoningContent != "" {
				for _, rLine := range strings.Split(msg.ReasoningContent, "\n") {
					msgLines = append(msgLines, "| "+rLine)
				}
			}
			msgLines = append(msgLines, "Assistant:")
			if len(msg.Blocks) > 0 {
				blockLines := renderBlocks(msg.Blocks, false)
				msgLines = append(msgLines, blockLines...)
			} else if msg.Content != "" {
				msgLines = append(msgLines, msg.Content)
			}
		case "system":
			msgLines = append(msgLines, "→ "+msg.Content)
		}
	}
	return msgLines
}

// --- Tests ---

func TestAutoScrollFillsViewport(t *testing.T) {
	// Fill viewport with system messages until full
	m := scrollTestModel(0)
	m = renderView(m)

	// Start adding system messages one at a time
	for i := 0; i < 20; i++ {
		m.messages = append(m.messages, chatMessage{Role: "system", Content: "msg"})
		m = renderView(m)
	}

	// After adding 20 messages to a 5-line viewport, we should be at the bottom
	if !m.vp.AtBottom() {
		t.Errorf("expected AtBottom after filling viewport with messages")
	}
}

func TestAutoScrollSystemMsgWhenAtBottom(t *testing.T) {
	// 3 messages, viewport shows 5 lines — not full yet, definitely at bottom
	m := scrollTestModel(3)
	m = renderView(m)

	if !m.vp.AtBottom() {
		t.Fatalf("setup: expected AtBottom, viewport has room")
	}

	// Add a system message
	m.messages = append(m.messages, chatMessage{Role: "system", Content: "new"})
	m = renderView(m)

	if !m.vp.AtBottom() {
		t.Errorf("expected AtBottom after adding message when already at bottom")
	}
}

func TestAutoScrollNoScrollWhenScrolledUp(t *testing.T) {
	// Many messages — viewport can't fit them all
	m := scrollTestModel(30)
	m = renderView(m)

	// Scroll up to see older messages
	m.vp.LineUp(2)
	if m.vp.AtBottom() {
		t.Skip("viewport didn't scroll up — reduce viewport height")
	}

	// Now add a system message — should NOT scroll
	m.messages = append(m.messages, chatMessage{Role: "system", Content: "new"})
	beforeY := m.vp.YOffset
	m = renderView(m)

	if m.vp.YOffset != beforeY {
		t.Errorf("expected YOffset to stay same when scrolled up, was %d, now %d",
			beforeY, m.vp.YOffset)
	}
}

func TestAutoScrollStreamMsgWhenAtBottom(t *testing.T) {
	m := scrollTestModel(3)
	m = renderView(m)

	if !m.vp.AtBottom() {
		t.Fatalf("setup: expected AtBottom")
	}

	// Simulate a streaming message (StreamMsg in update, then renderView)
	m.messages = append(m.messages, chatMessage{Role: "assistant", Streaming: true, Content: "token1"})
	m = renderView(m)

	if !m.vp.AtBottom() {
		t.Errorf("expected AtBottom after streaming token")
	}

	// More streaming tokens — still at bottom
	m.messages[len(m.messages)-1].Content += " token2"
	m = renderView(m)

	if !m.vp.AtBottom() {
		t.Errorf("expected AtBottom after second streaming token")
	}
}

func TestAutoScrollStreamDoneWhenAtBottom(t *testing.T) {
	m := scrollTestModel(3)
	m.messages = append(m.messages, chatMessage{Role: "assistant", Streaming: true, Content: "**bold** done"})
	m = renderView(m)

	// StreamDone: set Blocks
	msg := &m.messages[len(m.messages)-1]
	msg.Streaming = false
	msg.Blocks = parseMarkdown("**bold** done")
	m = renderView(m)

	if !m.vp.AtBottom() {
		t.Errorf("expected AtBottom after StreamDone")
	}
}

func TestAutoScrollFillAndKeepScrolling(t *testing.T) {
	// Fill viewport, then keep adding messages — should stay at bottom
	m := scrollTestModel(5)
	m = renderView(m)

	for i := 0; i < 30; i++ {
		m.messages = append(m.messages, chatMessage{Role: "system", Content: "fill"})
		m = renderView(m)

		if !m.vp.AtBottom() {
			t.Errorf("lost bottom at iteration %d", i)
			break
		}
	}
}

func TestAutoScrollScrolledUpThenScrollBack(t *testing.T) {
	m := scrollTestModel(30)
	m = renderView(m)

	// Scroll up
	m.vp.LineUp(5)
	beforeY := m.vp.YOffset

	// Add message while scrolled up — should not move
	m.messages = append(m.messages, chatMessage{Role: "system", Content: "hidden"})
	m = renderView(m)

	if m.vp.YOffset != beforeY {
		t.Fatalf("scroll jumped when scrolled up: was %d, now %d", beforeY, m.vp.YOffset)
	}

	// Now scroll back to bottom
	m.vp.GotoBottom()
	if !m.vp.AtBottom() {
		t.Fatalf("failed to scroll to bottom")
	}

	// Add another message — should auto-scroll
	m.messages = append(m.messages, chatMessage{Role: "system", Content: "visible"})
	m = renderView(m)

	if !m.vp.AtBottom() {
		t.Errorf("expected AtBottom after adding message when user returned to bottom")
	}
}

func TestAutoScrollWithMouseWheel(t *testing.T) {
	m := scrollTestModel(30)
	m = renderView(m)

	// Scroll up with mouse wheel
	m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if m.vp.AtBottom() {
		t.Skip("wheel scroll didn't move — viewport may be too small")
	}
	scrolledY := m.vp.YOffset

	// Add message while scrolled up — should not scroll
	m.messages = append(m.messages, chatMessage{Role: "system", Content: "new"})
	m = renderView(m)

	if m.vp.YOffset != scrolledY {
		t.Errorf("expected no scroll after wheel-up and new message, was %d, now %d",
			scrolledY, m.vp.YOffset)
	}
}
