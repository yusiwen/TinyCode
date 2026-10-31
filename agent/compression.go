package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/yusiwen/tinycode/types"
)

// CompressedSummary holds the result of a history compression operation.
type CompressedSummary struct {
	Summary string
}

// EstimateTokens returns a rough token count for text.
func EstimateTokens(text string) int {
	return len(text) / 4
}

// EstimateMessagesTokens estimates the total token count of a message list.
func EstimateMessagesTokens(msgs []types.Message) int {
	total := 0
	for _, m := range msgs {
		total += EstimateTokens(m.Content)
		total += EstimateTokens(m.ReasoningContent)
		for _, tc := range m.ToolCalls {
			total += EstimateTokens(tc.Name)
			total += EstimateTokens(tc.Arguments)
		}
		if m.Role != "" {
			total += 4
		}
	}
	total += len(msgs) * 10
	return total
}

// summarizeText asks the summarizer model for one summary and accounts for the
// call. It is the only place the compression path talks to a provider, so the
// spend it causes is counted in one place: before this, a summarizer call was
// invisible to UsageTotal, the budgets and the status bar.
func (a *Agent) summarizeText(ctx context.Context, instruction, text string, maxTokens, step int) (string, error) {
	// The summarizer runs on the same route the agent's calls go to: a pinned
	// agent should not summarize through a different provider than the one it
	// works on (roadmap A5).
	provider, err := a.chatProvider()
	if err != nil {
		return "", err
	}
	summarizer := &Agent{
		Provider: provider,
		Config: &AgentConfig{
			Name:     "compact",
			Mode:     AgentModePrimary,
			MaxSteps: 1,
		},
		Tools: nil,
	}
	req := types.ChatRequest{
		Messages: []types.Message{
			{Role: types.RoleSystem, Content: "You are a helpful assistant that summarizes conversations concisely."},
			{Role: types.RoleUser, Content: instruction + "\n\n" + text},
		},
		MaxTokens: maxTokens,
	}
	started := a.now()
	resp, err := summarizer.Provider.Chat(ctx, req)
	if err != nil {
		return "", err
	}
	a.recordCall(nil, resp, started, step)
	return resp.Content, nil
}

// describeMessages renders messages for a summarizer, truncating tool output to
// 200 characters: the point is the shape of what happened, not the bytes.
func describeMessages(messages []types.Message) string {
	var b strings.Builder
	for _, m := range messages {
		switch m.Role {
		case types.RoleUser:
			fmt.Fprintf(&b, "User: %s\n", m.Content)
		case types.RoleAssistant:
			if m.Content != "" {
				fmt.Fprintf(&b, "Assistant: %s\n", m.Content)
			}
			for _, call := range m.ToolCalls {
				fmt.Fprintf(&b, "Assistant called %s(%s)\n", call.Name, clipBytes(call.Arguments, 120))
			}
			if m.ReasoningContent != "" {
				fmt.Fprintf(&b, "Reasoning: %s\n", m.ReasoningContent)
			}
		case types.RoleTool:
			fmt.Fprintf(&b, "Tool (%s): %s\n", m.Name, clipBytes(m.Content, 200))
		case types.RoleSystem:
			// Preserve earlier summaries so compressing twice does not discard
			// the context that was already distilled.
			fmt.Fprintf(&b, "Context: %s\n", m.Content)
		}
	}
	return b.String()
}

// groupPinned reports whether any message in a group carries a pin. One pinned
// message pins the group: a user instruction whose answer was summarized away
// is not useful on its own, and a turn split in half is not something the
// compressor can reason about.
func groupPinned(msgs []types.Message) bool {
	for _, m := range msgs {
		if m.Pinned {
			return true
		}
	}
	return false
}

// groupEnd returns the index just past the message group containing idx. A
// group starts at a user message and runs until the next user message, so an
// assistant message with tool_calls always stays together with its tool
// results. Returns len(messages) when no later user message exists.
func groupEnd(messages []types.Message, idx int) int {
	if idx < 0 {
		return 0
	}
	for i := idx + 1; i < len(messages); i++ {
		if messages[i].Role == types.RoleUser {
			return i
		}
	}
	return len(messages)
}

// compressionPlan is the partition compressHistory makes, computed without a
// model call. The preview and the compression both read it, so they cannot
// disagree about what is kept (roadmap B5, S3).
type compressionPlan struct {
	// Head and Tail are kept verbatim: the first two user groups and the last two.
	Head []types.Message
	Tail []types.Message
	// Middle is the unpinned middle — what a summarizer would be given.
	Middle []types.Message
	// Pinned are middle groups the user asked to keep verbatim.
	Pinned []types.Message
	// Tokens is the history's estimated size and Threshold the bound that decides
	// whether automatic compression fires.
	Tokens    int
	Threshold int
}

// OverThreshold reports whether automatic compression would fire.
func (p compressionPlan) OverThreshold() bool {
	return p.Threshold > 0 && p.Tokens >= p.Threshold
}

// planCompression works out what a compression of these messages would keep and
// what it would summarize. The second result is the reason there is no plan, so
// a caller can say why instead of "nothing to do".
func (a *Agent) planCompression(messages []types.Message) (compressionPlan, string) {
	if a.CompressionThreshold <= 0 || a.ContextLength <= 0 {
		return compressionPlan{}, "compression is disabled"
	}
	plan := compressionPlan{Tokens: EstimateMessagesTokens(messages), Threshold: a.CompressionThreshold}

	var userMsgIndices []int
	for i, m := range messages {
		if m.Role == types.RoleUser {
			userMsgIndices = append(userMsgIndices, i)
		}
	}
	if len(userMsgIndices) < 4 {
		return compressionPlan{}, "the history has fewer than four user turns"
	}
	protectFirst := 2
	if protectFirst > len(userMsgIndices) {
		protectFirst = len(userMsgIndices)
	}
	// Snap the head cut forward to a message-group boundary. A fixed offset can
	// split an assistant message with tool_calls from its tool results, and an
	// assistant tool_calls message whose tool responses are missing is rejected
	// by OpenAI-compatible APIs.
	headEnd := groupEnd(messages, userMsgIndices[protectFirst-1]+2)
	if headEnd > len(messages) {
		headEnd = len(messages)
	}
	tailBudget := 2
	tailStart := userMsgIndices[len(userMsgIndices)-tailBudget]
	if tailStart <= headEnd {
		return compressionPlan{}, "the head and the tail overlap: there is no middle to compress"
	}
	middle := messages[headEnd:tailStart]
	if len(middle) < 2 {
		return compressionPlan{}, "the middle is too small to be worth a summary"
	}
	plan.Head = messages[:headEnd]
	plan.Tail = messages[tailStart:]

	// A pinned group is context the user asked to keep verbatim: it is never
	// fed to the summarizer, and it is put back after the summary and before the
	// tail. The order is therefore head, summary, pinned, recent tail — a
	// summary is already a lossy stand-in, and keeping the pinned text at all
	// matters more than where in the stand-in it sits (roadmap B5).
	for i := 0; i < len(middle); {
		end := groupEnd(middle, i)
		if groupPinned(middle[i:end]) {
			plan.Pinned = append(plan.Pinned, middle[i:end]...)
			i = end
			continue
		}
		plan.Middle = append(plan.Middle, middle[i:end]...)
		i = end
	}
	if len(plan.Middle) == 0 {
		return compressionPlan{}, "every turn in the middle is pinned, so a summary would be of nothing"
	}
	return plan, ""
}

// compressHistory compresses a.History when it exceeds the threshold. It
// returns the history unchanged (and nil error) when there is nothing to
// compress, and a nil history plus the summarizer's error when the provider
// call fails or the context is cancelled.
func (a *Agent) compressHistory(ctx context.Context, messages []types.Message) ([]types.Message, error) {
	plan, _ := a.planCompression(messages)
	if plan.Threshold == 0 || !plan.OverThreshold() {
		return messages, nil
	}
	head, middleText, pinned, tail := plan.Head, describeMessages(plan.Middle), plan.Pinned, plan.Tail

	const instruction = `Summarize the following conversation history. Focus on:
- What tasks were completed
- What decisions were made
- What files were modified or created
- Any pending issues or remaining work

Conversation to summarize:`

	// A summarizer failure is reported rather than swallowed: the caller keeps
	// its history either way, but only this way can it tell a cancelled run
	// (context.Canceled) from a healthy no-op.
	summary, err := a.summarizeText(ctx, instruction, middleText, 1024, 0)
	if err != nil {
		return nil, err
	}
	if summary == "" {
		return messages, nil
	}

	summaryMsg := types.Message{
		Role:    types.RoleSystem,
		Content: fmt.Sprintf("[COMPRESSED HISTORY — REFERENCE ONLY]\n%s\n[END COMPRESSED HISTORY]", summary),
	}

	compressed := make([]types.Message, 0, len(head)+1+len(pinned)+len(tail))
	compressed = append(compressed, head...)
	compressed = append(compressed, summaryMsg)
	compressed = append(compressed, pinned...)
	compressed = append(compressed, tail...)

	// Inject active todo items after compression so LLM doesn't redo completed work
	if a.TodoStorer != nil {
		if snapshot := a.TodoStorer.FormatForInjection(); snapshot != "" {
			compressed = append(compressed, types.Message{
				Role:    types.RoleSystem,
				Content: "[ACTIVE TODO ITEMS]\n" + snapshot,
			})
		}
	}

	return compressed, nil
}

// --- Context length resolution from API errors ---

var contextLengthPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:max(?:imum)?|limit)\s*(?:context\s*)?(?:length|size|window)?\s*(?:is|of|:)?\s*(\d{4,})`),
	regexp.MustCompile(`(?i)context\s*(?:length|size|window)\s*(?:is|of|:)?\s*(\d{4,})`),
	regexp.MustCompile(`(?i)(\d{4,})\s*(?:token)?\s*(?:context|limit)`),
	regexp.MustCompile(`(?i)>\s*(\d{4,})\s*(?:max|limit|token)`),
	regexp.MustCompile(`(?i)(\d{4,})\s*max(?:imum)?\b`),
}

// ParseContextLimitFromError extracts a context length limit from an API error message.
// Returns 0 if no limit is found.
func ParseContextLimitFromError(errMsg string) int {
	for _, re := range contextLengthPatterns {
		matches := re.FindStringSubmatch(errMsg)
		if len(matches) >= 2 {
			limit := 0
			fmt.Sscanf(matches[1], "%d", &limit)
			if limit >= 1024 && limit <= 10_000_000 {
				return limit
			}
		}
	}
	return 0
}

// HandleContextError checks if an error from the LLM API is a context_length_exceeded
// error. If so, it lowers the effective context length to avoid repeated failures.
// Returns true if the error was handled (caller should retry with compression).
func (a *Agent) HandleContextError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "context_length") &&
		!strings.Contains(errMsg, "context length") &&
		!strings.Contains(errMsg, "max_tokens") &&
		!strings.Contains(errMsg, "maximum context") {
		return false
	}

	limit := ParseContextLimitFromError(errMsg)
	if limit <= 0 {
		return false
	}

	// Only lower, never raise
	if limit < a.ContextLength && limit > 0 {
		a.discoveredCtxLen = limit
		// Recalculate threshold: 50% of discovered limit
		a.CompressionThreshold = limit / 2
		return true
	}
	return false
}

// EffectiveContextLength returns the current effective context length
// (lowest of configured and discovered).
func (a *Agent) EffectiveContextLength() int {
	if a.discoveredCtxLen > 0 && a.discoveredCtxLen < a.ContextLength {
		return a.discoveredCtxLen
	}
	return a.ContextLength
}
