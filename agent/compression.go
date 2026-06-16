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

// compressHistory compresses a.History when it exceeds the threshold.
func (a *Agent) compressHistory(messages []types.Message) ([]types.Message, error) {
	if a.CompressionThreshold <= 0 || a.ContextLength <= 0 {
		return messages, nil
	}
	estimatedTokens := EstimateMessagesTokens(messages)
	if estimatedTokens < a.CompressionThreshold {
		return messages, nil
	}
	var userMsgIndices []int
	for i, m := range messages {
		if m.Role == types.RoleUser {
			userMsgIndices = append(userMsgIndices, i)
		}
	}
	if len(userMsgIndices) < 4 {
		return messages, nil
	}
	protectFirst := 2
	if protectFirst > len(userMsgIndices) {
		protectFirst = len(userMsgIndices)
	}
	headEnd := userMsgIndices[protectFirst-1] + 3
	if headEnd >= len(messages) {
		headEnd = len(messages)
	}
	tailBudget := 2
	tailStart := userMsgIndices[len(userMsgIndices)-tailBudget]
	if tailStart <= headEnd {
		return messages, nil
	}
	head := messages[:headEnd]
	middle := messages[headEnd:tailStart]
	tail := messages[tailStart:]
	if len(middle) < 2 {
		return messages, nil
	}

	var middleText strings.Builder
	for _, m := range middle {
		switch m.Role {
		case types.RoleUser:
			middleText.WriteString(fmt.Sprintf("User: %s\n", m.Content))
		case types.RoleAssistant:
			if m.Content != "" {
				middleText.WriteString(fmt.Sprintf("Assistant: %s\n", m.Content))
			}
			if m.ReasoningContent != "" {
				middleText.WriteString(fmt.Sprintf("Reasoning: %s\n", m.ReasoningContent))
			}
		case types.RoleTool:
			content := m.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			middleText.WriteString(fmt.Sprintf("Tool (%s): %s\n", m.Name, content))
		}
	}

	summaryPrompt := fmt.Sprintf(`Summarize the following conversation history. Focus on:
- What tasks were completed
- What decisions were made
- What files were modified or created
- Any pending issues or remaining work

Conversation to summarize:
%s

Provide a concise summary in 3-5 sentences.`, middleText.String())

	summarizer := &Agent{
		Provider: a.Provider,
		Config: &AgentConfig{
			Name:    "compact",
			Mode:    AgentModePrimary,
			MaxSteps: 1,
		},
		Tools: nil,
	}
	summarizerReq := types.ChatRequest{
		Messages: []types.Message{
			{Role: types.RoleSystem, Content: "You are a helpful assistant that summarizes conversations concisely."},
			{Role: types.RoleUser, Content: summaryPrompt},
		},
		MaxTokens: 1024,
	}
	resp, err := summarizer.Provider.Chat(context.Background(), summarizerReq)
	if err != nil {
		return messages, nil
	}
	summary := resp.Content
	if summary == "" {
		return messages, nil
	}

	summaryMsg := types.Message{
		Role:    types.RoleSystem,
		Content: fmt.Sprintf("[COMPRESSED HISTORY — REFERENCE ONLY]\n%s\n[END COMPRESSED HISTORY]", summary),
	}

	compressed := make([]types.Message, 0, len(head)+1+len(tail))
	compressed = append(compressed, head...)
	compressed = append(compressed, summaryMsg)
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
