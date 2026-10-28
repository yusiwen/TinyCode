package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// compactRunMessages compresses the *run's own* traffic — the assistant and tool
// messages between the prompt and now — when it crosses the compression
// threshold (roadmap B5, S2).
//
// compressHistory works on a.History between runs; the loop's messages grow
// inside one run and were never compressed, so a long round grew until the
// provider refused. That is the failure D7's continuation rounds make routine.
//
// The cut snaps forward to an assistant message: each round of the loop starts
// with one, and a tool_calls assistant separated from its tool results is
// rejected by the provider.
func (a *Agent) compactRunMessages(ctx context.Context, messages []types.Message, step int) ([]types.Message, bool) {
	if a.CompressionThreshold <= 0 || a.ContextLength <= 0 {
		return messages, false
	}
	if EstimateMessagesTokens(messages) < a.CompressionThreshold {
		return messages, false
	}
	// The current run's prompt is the last user message: the loop appends only
	// assistant and tool messages after it.
	prompt := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == types.RoleUser {
			prompt = i
			break
		}
	}
	if prompt < 0 {
		return messages, false
	}
	rest := messages[prompt+1:]
	// The recent tail is kept verbatim: it is what the next call reasons about.
	const keepTail = 8
	if len(rest) <= keepTail+2 {
		return messages, false
	}
	cut := len(rest) - keepTail
	for cut < len(rest) && rest[cut].Role != types.RoleAssistant {
		cut++
	}
	if cut <= 0 || cut >= len(rest) {
		return messages, false
	}

	const instruction = `Summarize what this run has done so far. Focus on:
- The tools that were called and what they established
- Files read or modified, and why
- What is still open

Run so far:`
	summary, err := a.summarizeText(ctx, instruction, describeMessages(rest[:cut]), 1024, step)
	if err != nil {
		// A failed compaction costs the run one step of context, not the run:
		// the messages stay as they were and the loop continues.
		tlog.Warn("agent.loop", "run compaction skipped", "error", err)
		return messages, false
	}
	if strings.TrimSpace(summary) == "" {
		return messages, false
	}

	out := make([]types.Message, 0, len(messages)-cut+1)
	out = append(out, messages[:prompt+1]...)
	out = append(out, types.Message{
		Role:    types.RoleSystem,
		Content: fmt.Sprintf("[COMPACTED RUN — REFERENCE ONLY]\n%s\n[END COMPACTED RUN]", summary),
	})
	out = append(out, rest[cut:]...)
	tlog.Info("agent.loop", "compacted run messages", "summarized", cut, "kept", len(rest)-cut, "step", step)
	return out, true
}
