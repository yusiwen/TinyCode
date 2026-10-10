package tui

import (
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// usageModel is a model mid-stream, like ChatMsg leaves it.
func usageModel() *TuiModel {
	m := streamModel()
	m.messages = append(m.messages, chatMessage{Role: "assistant", Streaming: true})
	m.curAssistant = &m.messages[len(m.messages)-1]
	m.status = StatusStreaming
	return m
}

// TestReportedUsageReplacesTheEstimate pins the swap: the provider's own number
// is what the status bar shows, not len(text)/4.
func TestReportedUsageReplacesTheEstimate(t *testing.T) {
	m := usageModel()
	for i := 0; i < 40; i++ {
		m.Update(StreamMsg{TextDelta: "a"}) // 40 bytes → 10 estimated tokens
	}
	if m.sessionTokens != 10 {
		t.Fatalf("sessionTokens = %d, want the 10-token estimate", m.sessionTokens)
	}

	m.Update(UsageMsg{Usage: types.Usage{PromptTokens: 900, CompletionTokens: 12, TotalTokens: 912}})

	if m.sessionTokens != 912 {
		t.Errorf("sessionTokens = %d, want the reported total 912", m.sessionTokens)
	}
	if m.callTokens != 0 {
		t.Errorf("callTokens = %d, want 0 after the estimate was replaced", m.callTokens)
	}
	if bar := m.renderStatusBar(); !strings.Contains(bar, "tokens: 912") {
		t.Errorf("status bar does not show the reported total: %q", bar)
	}
}

// TestUnreportedCallKeepsItsEstimate is the fallback: an endpoint that reports
// nothing sends no UsageMsg, so what was estimated has to stay in the counter
// and must not be taken back by the next call's correction.
func TestUnreportedCallKeepsItsEstimate(t *testing.T) {
	m := usageModel()
	for i := 0; i < 40; i++ {
		m.Update(StreamMsg{TextDelta: "a"}) // 10 estimated tokens, never reported
	}
	m.Update(StreamDone{IsIntermediate: true})

	if m.sessionTokens != 10 {
		t.Fatalf("sessionTokens = %d, want the unreported estimate to survive the step boundary", m.sessionTokens)
	}
	if m.callTokens != 0 {
		t.Fatalf("callTokens = %d, want 0 at the step boundary", m.callTokens)
	}

	// The second call streams 80 bytes (20 tokens) and reports 30.
	m.curAssistant = &m.messages[len(m.messages)-1]
	for i := 0; i < 80; i++ {
		m.Update(StreamMsg{TextDelta: "b"})
	}
	m.Update(UsageMsg{Usage: types.Usage{PromptTokens: 5, CompletionTokens: 25, TotalTokens: 30}})

	if m.sessionTokens != 40 {
		t.Errorf("sessionTokens = %d, want 10 (unreported estimate) + 30 (reported)", m.sessionTokens)
	}
}

// TestReportedUsageAccumulatesAcrossCalls keeps the session total additive: each
// reported call contributes its own number once.
func TestReportedUsageAccumulatesAcrossCalls(t *testing.T) {
	m := usageModel()
	for i := 0; i < 40; i++ {
		m.Update(StreamMsg{TextDelta: "a"})
	}
	m.Update(UsageMsg{Usage: types.Usage{PromptTokens: 80, CompletionTokens: 20, TotalTokens: 100}})
	if m.sessionTokens != 100 {
		t.Fatalf("sessionTokens = %d, want 100 after the first reported call", m.sessionTokens)
	}

	m.Update(StreamDone{IsIntermediate: true})
	m.curAssistant = &m.messages[len(m.messages)-1]
	for i := 0; i < 40; i++ {
		m.Update(StreamMsg{TextDelta: "b"})
	}
	m.Update(UsageMsg{Usage: types.Usage{PromptTokens: 150, CompletionTokens: 50, TotalTokens: 200}})

	if m.sessionTokens != 300 {
		t.Errorf("sessionTokens = %d, want 100 + 200", m.sessionTokens)
	}
}

// TestUsageMsgFromSupersededRunIsIgnored keeps a late report from a cancelled
// run out of the current session's counters.
func TestUsageMsgFromSupersededRunIsIgnored(t *testing.T) {
	m := usageModel()
	m.runActive = true
	m.runID = 7

	m.Update(UsageMsg{RunID: 6, Usage: types.Usage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20}})

	if m.sessionTokens != 0 {
		t.Errorf("sessionTokens = %d, want a superseded run's usage ignored", m.sessionTokens)
	}
}

// TestResetSessionStatsClearsTheCallShare makes sure a transcript swap cannot
// leave a stale call estimate behind to be subtracted from a later report.
func TestResetSessionStatsClearsTheCallShare(t *testing.T) {
	m := usageModel()
	for i := 0; i < 40; i++ {
		m.Update(StreamMsg{TextDelta: "a"})
	}
	if m.callTokens == 0 {
		t.Fatal("callTokens was never charged, so this test proves nothing")
	}

	m.resetSessionStats()

	if m.sessionTokens != 0 || m.callTokens != 0 {
		t.Errorf("counters not cleared: tokens=%d callTokens=%d", m.sessionTokens, m.callTokens)
	}
}
