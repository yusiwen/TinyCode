package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/yusiwen/tinycode/types"
)

// previewLines bounds how many messages one section lists. The preview is meant
// to be read in a terminal, not to reproduce the history.
const previewLines = 12

// CompressionPreview describes what a compression of the current history would
// keep and what it would summarize, without calling a model (roadmap B5, S3).
//
// It reads the same partition compressHistory uses — planCompression — so the
// preview cannot disagree with the thing it previews about which turns are kept.
func (a *Agent) CompressionPreview() string {
	plan, reason := a.planCompression(a.History)
	if plan.Threshold == 0 {
		return "Compression preview: nothing to show — " + reason + "."
	}
	var b strings.Builder
	state := "would not fire (under the threshold)"
	if plan.OverThreshold() {
		state = "would fire"
	}
	fmt.Fprintf(&b, "Compression preview (no model call): %d messages, ~%d tokens, threshold %d — automatic compression %s.\n",
		len(a.History), plan.Tokens, plan.Threshold, state)
	if reason != "" {
		fmt.Fprintf(&b, "Nothing would be compressed: %s.\n", reason)
		return b.String()
	}
	writePreviewSection(&b, "kept: head", plan.Head)
	fmt.Fprintf(&b, "summarized: middle — %d messages, ~%d tokens\n",
		len(plan.Middle), EstimateMessagesTokens(plan.Middle))
	writePreviewSection(&b, "kept: pinned", plan.Pinned)
	writePreviewSection(&b, "kept: tail", plan.Tail)
	return b.String()
}

// writePreviewSection lists one kept region: its size and the first line of each
// message, capped so the preview stays a glance.
func writePreviewSection(b *strings.Builder, label string, messages []types.Message) {
	if len(messages) == 0 {
		return
	}
	fmt.Fprintf(b, "%s — %d messages, ~%d tokens\n", label, len(messages), EstimateMessagesTokens(messages))
	for i, msg := range messages {
		if i == previewLines {
			fmt.Fprintf(b, "  … (%d more)\n", len(messages)-i)
			break
		}
		fmt.Fprintf(b, "  %s: %s\n", msg.Role, firstLine(msg.Content, 60))
	}
}

// firstLine returns the first non-empty line of text, clipped to max runes.
func firstLine(text string, max int) string {
	trimmed := strings.TrimSpace(text)
	for _, line := range strings.Split(trimmed, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if utf8.RuneCountInString(line) > max {
				runes := []rune(line)
				return string(runes[:max]) + "…"
			}
			return line
		}
	}
	return "(no text)"
}
