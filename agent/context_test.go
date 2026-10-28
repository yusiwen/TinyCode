package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// turnHistory builds n turns of two messages each: "question i" then "answer i".
func turnHistory(n int) []types.Message {
	history := make([]types.Message, 0, 2*n)
	for i := 0; i < n; i++ {
		history = append(history,
			types.Message{Role: types.RoleUser, Content: "question " + string(rune('0'+i))},
			types.Message{Role: types.RoleAssistant, Content: "answer " + string(rune('0'+i))},
		)
	}
	return history
}

// TestPinLastMarksExactlyTheLastTurns pins the unit these commands work in: a
// turn is a user message and everything up to the next one.
func TestPinLastMarksExactlyTheLastTurns(t *testing.T) {
	ag := New(nil)
	ag.History = turnHistory(4)

	pinned, total := ag.PinLast(2)
	if pinned != 2 || total != 2 {
		t.Fatalf("pinned=%d total=%d, want 2/2", pinned, total)
	}
	for i, msg := range ag.History {
		wantPinned := i >= 4 // the last two turns are indices 4..7
		if msg.Pinned != wantPinned {
			t.Fatalf("message %d (%s) pinned=%v, want %v", i, msg.Role, msg.Pinned, wantPinned)
		}
	}

	// Asking for more turns than exist pins what there is and says so.
	pinned, total = ag.PinLast(10)
	if pinned != 4 || total != 4 {
		t.Fatalf("pinning 10 turns pinned=%d total=%d, want the 4 that exist", pinned, total)
	}
}

// TestUnpinLastClearsNewestFirst pins the order: the turn a user just pinned is
// the one they mean when they say "unpin".
func TestUnpinLastClearsNewestFirst(t *testing.T) {
	ag := New(nil)
	ag.History = turnHistory(3)
	ag.PinLast(3)

	cleared, remaining := ag.UnpinLast(1)
	if cleared != 1 || remaining != 2 {
		t.Fatalf("cleared=%d remaining=%d, want 1/2", cleared, remaining)
	}
	// The newest turn (indices 4,5) lost its pin; the older ones kept theirs.
	if ag.History[4].Pinned || ag.History[5].Pinned {
		t.Fatal("the newest turn is still pinned")
	}
	if !ag.History[0].Pinned || !ag.History[2].Pinned {
		t.Fatal("unpinning one turn cleared an older pin")
	}

	if cleared, remaining := ag.UnpinLast(5); cleared != 2 || remaining != 0 {
		t.Fatalf("cleared=%d remaining=%d, want 2/0", cleared, remaining)
	}
	if cleared, _ := ag.UnpinLast(1); cleared != 0 {
		t.Fatalf("unpinning with no pins cleared %d", cleared)
	}
}

// TestDropLastRemovesExactlyTheLastTurns also pins that the original history is
// not written through: a goroutine holding the old slice must not see its
// contents replaced.
func TestDropLastRemovesExactlyTheLastTurns(t *testing.T) {
	ag := New(nil)
	original := turnHistory(4)
	ag.History = append([]types.Message(nil), original...)

	dropped := ag.DropLast(2)
	if dropped != 2 {
		t.Fatalf("dropped=%d, want 2", dropped)
	}
	if len(ag.History) != 4 {
		t.Fatalf("history has %d messages, want 4", len(ag.History))
	}
	if ag.History[3].Content != "answer 1" {
		t.Fatalf("the wrong turns were removed: %+v", ag.History)
	}
	if original[6].Content != "question 3" {
		t.Fatal("dropping rewrote the slice it was given")
	}

	if dropped := ag.DropLast(5); dropped != 2 {
		t.Fatalf("dropping more turns than exist removed %d, want the 2 that exist", dropped)
	}
	if len(ag.History) != 0 {
		t.Fatalf("history is %d messages, want empty", len(ag.History))
	}
	if dropped := ag.DropLast(1); dropped != 0 {
		t.Fatalf("dropping from an empty history removed %d", dropped)
	}
}

// TestCompressionKeepsPinnedGroupsVerbatim is the point of the whole change: a
// pinned turn is neither summarized nor summarized-away, and the summarizer
// never sees it.
func TestCompressionKeepsPinnedGroupsVerbatim(t *testing.T) {
	provider := &recordingProvider{content: "SUMMARY"}
	ag := New(provider)
	ag.ContextLength = 1000
	ag.CompressionThreshold = 1 // compress whatever it is given
	// Eight turns: the head keeps turns 1-3, the tail turns 7-8, so the middle
	// is turns 4-5 (indices 6-11). Pinning a head turn would prove nothing — the
	// head is kept whatever the pins say.
	ag.History = turnHistory(8)
	ag.History[6].Pinned = true // turn 4
	ag.History[7].Pinned = true

	compressed, err := ag.compressHistory(context.Background(), ag.History)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("the summarizer was called %d times, want 1", len(provider.requests))
	}
	if prompt := provider.requests[0].Messages[1].Content; strings.Contains(prompt, "answer 3") {
		t.Fatalf("the pinned turn was fed to the summarizer:\n%s", prompt)
	}
	if !containsMessage(compressed, "answer 3") {
		t.Fatal("the pinned turn is missing from the compressed history")
	}
	if !containsMessage(compressed, "SUMMARY") {
		t.Fatal("the summary is missing from the compressed history")
	}
	if containsMessage(compressed, "answer 4") {
		t.Fatal("the unpinned middle was not summarized away")
	}
}

// TestCompressionDoesNothingWhenEverythingIsPinned pins the cheap path: a middle
// that is all pinned must not cost a provider call.
func TestCompressionDoesNothingWhenEverythingIsPinned(t *testing.T) {
	provider := &recordingProvider{content: "SUMMARY"}
	ag := New(provider)
	ag.ContextLength = 1000
	ag.CompressionThreshold = 1
	ag.History = turnHistory(8)
	// The middle is turns 4 and 5 (indices 6-11); pin all of it. PinLast would
	// pin the tail, which is not the region the summarizer touches.
	for i := 6; i < 12; i++ {
		ag.History[i].Pinned = true
	}

	compressed, err := ag.compressHistory(context.Background(), ag.History)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("the summarizer was called %d times for a fully pinned middle", len(provider.requests))
	}
	if len(compressed) != len(ag.History) {
		t.Fatalf("history changed: %d messages, want %d", len(compressed), len(ag.History))
	}
}

// containsMessage reports whether any message in the history carries the text.
func containsMessage(history []types.Message, text string) bool {
	for _, msg := range history {
		if strings.Contains(msg.Content, text) {
			return true
		}
	}
	return false
}

// TestPinnedTurnTexts pins the view the TUI mirrors: the pinned turns' own text,
// in order, so a transcript can mark the same ones without index guessing.
func TestPinnedTurnTexts(t *testing.T) {
	ag := New(nil)
	ag.History = turnHistory(3)
	ag.PinLast(1)

	texts := ag.PinnedTurnTexts()
	if len(texts) != 1 {
		t.Fatalf("pinned texts = %+v, want one turn", texts)
	}
	if texts[0][0] != "question 2" || texts[0][1] != "answer 2" {
		t.Fatalf("pinned turn = %+v, want the last one", texts[0])
	}
	if got := ag.PinnedTurnCount(); got != 1 {
		t.Fatalf("PinnedTurnCount = %d, want 1", got)
	}
}
