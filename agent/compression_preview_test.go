package agent

import (
	"context"
	"strings"
	"testing"
)

// previewHistory is seven turns with distinctive text, the fourth pinned: with
// this shape the head is turns 1-3, the middle is turns 4-5 (one pinned, one
// not), and the tail is turns 6-7. That mix is what a preview has to report.
func previewHistory(t *testing.T, pinned bool) *Agent {
	t.Helper()
	ag := New(&recordingProvider{content: "SUMMARY"})
	ag.ContextLength = 1000
	ag.CompressionThreshold = 1
	ag.History = turnHistory(7)
	if pinned {
		ag.History[6].Pinned = true
		ag.History[7].Pinned = true
	}
	return ag
}

// TestCompressionPreviewCallsNoModel is the acceptance that matters most: a
// preview that asks a provider is not a preview, it is a compression.
func TestCompressionPreviewCallsNoModel(t *testing.T) {
	provider := &recordingProvider{content: "SUMMARY"}
	ag := New(provider)
	ag.ContextLength = 1000
	ag.CompressionThreshold = 1
	ag.History = turnHistory(6)

	if out := ag.CompressionPreview(); !strings.Contains(out, "Compression preview") {
		t.Fatalf("preview = %q", out)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("the preview called the provider %d times", len(provider.requests))
	}
}

// TestCompressionPreviewNamesGroups pins the report's shape: each kept region is
// named with its messages' first lines, and the middle is reported by size.
func TestCompressionPreviewNamesGroups(t *testing.T) {
	ag := previewHistory(t, true)

	out := ag.CompressionPreview()
	for _, want := range []string{
		"kept: head", "kept: pinned", "kept: tail",
		"summarized: middle",
		"user: question 0",    // head
		"assistant: answer 2", // head
		"user: question 3",    // pinned (turn 4)
		"assistant: answer 3",
		"user: question 5", // tail
	} {
		if !strings.Contains(out, want) {
			t.Errorf("preview is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "middle — 2 messages") {
		t.Errorf("the middle should report the two messages it would summarize:\n%s", out)
	}
}

// TestCompressionPreviewSaysWhyNothingHappens: a history that cannot be
// compressed says why, instead of printing empty sections.
func TestCompressionPreviewSaysWhyNothingHappens(t *testing.T) {
	ag := New(&recordingProvider{content: "SUMMARY"})
	ag.ContextLength = 1000
	ag.CompressionThreshold = 1
	ag.History = turnHistory(3)

	out := ag.CompressionPreview()
	if !strings.Contains(out, "fewer than four user turns") {
		t.Fatalf("preview = %q", out)
	}

	ag.CompressionThreshold = 0
	if out := ag.CompressionPreview(); !strings.Contains(out, "compression is disabled") {
		t.Fatalf("disabled preview = %q", out)
	}
}

// TestCompressionPreviewMatchesTheCompression is the agreement the acceptance
// asks for: the plan the preview reads is the plan the compression executes.
func TestCompressionPreviewMatchesTheCompression(t *testing.T) {
	ag := previewHistory(t, true)
	plan, reason := ag.planCompression(ag.History)
	if reason != "" {
		t.Fatalf("no plan: %s", reason)
	}

	compressed, err := ag.compressHistory(context.Background(), ag.History)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if compressed == nil {
		t.Fatal("nothing was compressed")
	}
	// The output is head, summary, pinned, tail — exactly the plan's partition.
	if len(compressed) != len(plan.Head)+1+len(plan.Pinned)+len(plan.Tail) {
		t.Fatalf("compressed has %d messages, the plan says %d", len(compressed), len(plan.Head)+1+len(plan.Pinned)+len(plan.Tail))
	}
	for i, msg := range plan.Head {
		if compressed[i].Content != msg.Content {
			t.Fatalf("head message %d differs", i)
		}
	}
	summary := compressed[len(plan.Head)]
	if !strings.Contains(summary.Content, "SUMMARY") {
		t.Fatalf("the summary is not where the plan says: %+v", summary)
	}
	offset := len(plan.Head) + 1
	for i, msg := range plan.Pinned {
		if compressed[offset+i].Content != msg.Content {
			t.Fatalf("pinned message %d differs", i)
		}
	}
	offset += len(plan.Pinned)
	for i, msg := range plan.Tail {
		if compressed[offset+i].Content != msg.Content {
			t.Fatalf("tail message %d differs", i)
		}
	}
}

// TestCompressionPreviewUnderAndOverTheThreshold pins the one thing the report
// says about automatic compression: whether it would fire.
func TestCompressionPreviewUnderAndOverTheThreshold(t *testing.T) {
	ag := previewHistory(t, false)
	if out := ag.CompressionPreview(); !strings.Contains(out, "would fire") {
		t.Fatalf("over the threshold, preview says:\n%s", out)
	}
	ag.CompressionThreshold = 1000000
	if out := ag.CompressionPreview(); !strings.Contains(out, "would not fire") {
		t.Fatalf("under the threshold, preview says:\n%s", out)
	}
	// The partition is still reported under the threshold: the question "what
	// would be kept" has an answer even when nothing would run.
	if out := ag.CompressionPreview(); !strings.Contains(out, "kept: head") {
		t.Fatalf("under the threshold, the partition is missing:\n%s", out)
	}
}

// TestFirstLine pins the clipping the sections rely on.
func TestFirstLine(t *testing.T) {
	if got := firstLine("\n\n  hello world\nsecond", 60); got != "hello world" {
		t.Fatalf("firstLine = %q", got)
	}
	if got := firstLine("", 60); got != "(no text)" {
		t.Fatalf("empty firstLine = %q", got)
	}
	if got := firstLine(strings.Repeat("x", 70), 10); got != strings.Repeat("x", 10)+"…" {
		t.Fatalf("clipped firstLine = %q", got)
	}
}
