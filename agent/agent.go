package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/yusiwen/tinycode/types"
)

// Agent is the core ReAct loop.
type Agent struct {
	Provider   LLMProvider
	Tools      []Tool
	Memory     types.MemoryStore
	MemoryMode int // 0=none, 1=auto, 2=on-demand

	SessionStore interface {
		Append(msg types.Message) error
		Flush() error
	}
	History []types.Message // multi-turn conversation history

	SystemPrompt string
	MaxSteps     int
	MaxTokens    int
	Verbose      bool // when true, print detailed tool results
	ShowThinking bool // when true, display reasoning_content from thinking mode
}

// ANSI color codes for terminal output.
const (
	colorCyan    = "\033[36m"
	colorGray    = "\033[90m"
	colorYellow  = "\033[33m"
	colorDim     = "\033[2m"
	colorReset   = "\033[0m"
)

// stepName prints the step header (always visible) in cyan.
func (a *Agent) stepName(format string, args ...any) {
	fmt.Printf(colorCyan+"[tinycode] "+format+colorReset+"\n", args...)
}

// stepDetail prints detailed output in gray, only when Verbose is enabled.
func (a *Agent) stepDetail(format string, args ...any) {
	if a.Verbose {
		fmt.Printf(colorGray+"[tinycode] "+format+colorReset+"\n", args...)
	}
}

// showThinking prints the model's reasoning content in dim yellow with | prefix.
// Only shown when ShowThinking is enabled and reasoning_content is non-empty.
func (a *Agent) showThinking(reasoning string) {
	if !a.ShowThinking || reasoning == "" {
		return
	}
	for _, line := range strings.Split(strings.TrimRight(reasoning, "\n"), "\n") {
		fmt.Printf(colorDim + colorYellow + "| " + line + colorReset + "\n")
	}
}

const (
	MemoryModeNone     = 0
	MemoryModeAuto     = 1
	MemoryModeOnDemand = 2

	// securityBlockMarker is the prefix used by sandbox tools to indicate
	// a security restriction. The agent loop detects this and bypasses the LLM.
	securityBlockMarker = "[SECURITY BLOCKED]"
)

// New creates an Agent with sensible defaults.
func New(provider LLMProvider) *Agent {
	return &Agent{
		Provider:     provider,
		SystemPrompt: "You are TinyCode, an AI coding assistant. " +
			"Use tools when needed to accomplish the user's request. Think step by step.",
		MaxSteps:  20,
		MaxTokens: 4096,
	}
}

// AddTool registers a tool.
func (a *Agent) AddTool(t Tool) {
	a.Tools = append(a.Tools, t)
}

// Run executes the ReAct loop for a user prompt.
func (a *Agent) Run(ctx context.Context, prompt string) (string, error) {
	messages := []types.Message{
		{Role: types.RoleSystem, Content: a.SystemPrompt},
	}

	// Load multi-turn history, skipping messages that would cause API errors
	for _, msg := range a.History {
		// Skip assistant messages with neither content nor tool_call
		if msg.Role == types.RoleAssistant && msg.Content == "" && msg.ToolCall == nil {
			continue
		}
		messages = append(messages, msg)
	}

	// Inject long-term memory if enabled
	if a.Memory != nil && a.MemoryMode == MemoryModeAuto {
		memories, err := a.Memory.Recall(prompt, 5)
		if err == nil && len(memories) > 0 {
			var sb string
			for _, m := range memories {
				sb += fmt.Sprintf("- %s: %s\n", m.Key, m.Value)
			}
			messages = append(messages, types.Message{
				Role:    types.RoleSystem,
				Content: "Relevant memories:\n" + sb,
			})
		}
	}

	messages = append(messages, types.Message{Role: types.RoleUser, Content: prompt})

	step := 0
	for step < a.MaxSteps {
		toolDefs := make([]types.ToolDef, len(a.Tools))
		for i, t := range a.Tools {
			toolDefs[i] = types.ToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			}
		}

		resp, err := a.Provider.Chat(ctx, types.ChatRequest{
			Messages:  messages,
			Tools:     toolDefs,
			MaxTokens: a.MaxTokens,
		})
		if err != nil {
			return "", fmt.Errorf("LLM call failed: %w", err)
		}

		// Show reasoning content if enabled
		a.showThinking(resp.ReasoningContent)

		// No tool call → final answer
		if resp.ToolCall == nil {
			messages = append(messages, types.Message{
				Role:             types.RoleAssistant,
				Content:          resp.Content,
				ReasoningContent: resp.ReasoningContent,
			})
			// Save to multi-turn history (skip empty responses)
			if resp.Content != "" {
				a.History = append(a.History,
					types.Message{Role: types.RoleUser, Content: prompt},
					types.Message{Role: types.RoleAssistant, Content: resp.Content, ReasoningContent: resp.ReasoningContent},
				)
			}
			// Persist to disk if session store available
			if a.SessionStore != nil {
				a.SessionStore.Append(types.Message{Role: types.RoleUser, Content: prompt})
				a.SessionStore.Append(types.Message{Role: types.RoleAssistant, Content: resp.Content})
				if err := a.SessionStore.Flush(); err != nil {
					log.Printf("warning: flush session: %v", err)
				}
			}
			return resp.Content, nil
		}

		// Tool call
		a.stepName("[step %d] calling tool %s", step, resp.ToolCall.Name)

		messages = append(messages, types.Message{
			Role:             types.RoleAssistant,
			Content:          "",
			ReasoningContent: resp.ReasoningContent,
			ToolCall: &types.ToolCall{
				ID:        resp.ToolCall.ID,
				Name:      resp.ToolCall.Name,
				Arguments: resp.ToolCall.Arguments,
			},
		})

		var result string
		found := false
		for _, t := range a.Tools {
			if t.Name == resp.ToolCall.Name {
				found = true
				var args map[string]any
				if err := json.Unmarshal([]byte(resp.ToolCall.Arguments), &args); err != nil {
					result = fmt.Sprintf("error parsing args: %v", err)
				} else {
					var execErr error
					result, execErr = t.Execute(ctx, args)
					if execErr != nil {
						result = fmt.Sprintf("error: %v", execErr)
					}
				}
				break
			}
		}
		if !found {
			result = fmt.Sprintf("unknown tool: %s", resp.ToolCall.Name)
		}

		// Show tool input and result (verbose only)
		if len(resp.ToolCall.Arguments) > 500 {
			a.stepDetail("[step %d] tool input:\n%s...", step, resp.ToolCall.Arguments[:500])
		} else {
			a.stepDetail("[step %d] tool input:\n%s", step, resp.ToolCall.Arguments)
		}
		if len(result) > 500 {
			a.stepDetail("[step %d] tool result (%d chars):\n%s...", step, len(result), result[:500])
		} else {
			a.stepDetail("[step %d] tool result (%d chars):\n%s", step, len(result), result)
		}

		// Security intercept: if the tool result is a security block,
		// bypass the LLM and return directly to the user.
		// Use HasPrefix to avoid false matches on file content that happens
		// to contain the marker string (e.g. agent/agent.go defines the constant).
		isSecurityBlock := strings.HasPrefix(result, "\n"+securityBlockMarker) ||
			strings.HasPrefix(result, securityBlockMarker)

		if isSecurityBlock {
			a.stepName("[step %d] security block detected, bypassing LLM", step)
			if a.SessionStore != nil {
				a.SessionStore.Append(types.Message{Role: types.RoleUser, Content: prompt})
				a.SessionStore.Append(types.Message{Role: types.RoleAssistant, Content: result})
				a.SessionStore.Flush()
			}
			return result, nil
		}

		// Truncate large tool output (2000 lines / 50KB limit)
		// Full output is saved to disk; LLM reads remainder via read_file with offset/limit.
		trunc := TruncateOutput(result)
		truncatedResult := trunc.Content

		messages = append(messages, types.Message{
			Role:        types.RoleTool,
			Content:     truncatedResult,
			Name:        resp.ToolCall.Name,
			ToolCallID:  resp.ToolCall.ID,
		})
		step++
	}

	return "", fmt.Errorf("exceeded max steps (%d)", a.MaxSteps)
}
