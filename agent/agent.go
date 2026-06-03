package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

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
}

const (
	MemoryModeNone    = 0
	MemoryModeAuto    = 1
	MemoryModeOnDemand = 2
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

	// Load multi-turn history
	messages = append(messages, a.History...)

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

		// No tool call → final answer
		if resp.ToolCall == nil {
			messages = append(messages, types.Message{
				Role:    types.RoleAssistant,
				Content: resp.Content,
			})
			// Save to multi-turn history
			a.History = append(a.History,
				types.Message{Role: types.RoleUser, Content: prompt},
				types.Message{Role: types.RoleAssistant, Content: resp.Content},
			)
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
		log.Printf("[step %d] calling tool %s", step, resp.ToolCall.Name)

		messages = append(messages, types.Message{
			Role:    types.RoleAssistant,
			Content: "",
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

		messages = append(messages, types.Message{
			Role:        types.RoleTool,
			Content:     result,
			Name:        resp.ToolCall.Name,
			ToolCallID:  resp.ToolCall.ID,
		})
		step++
	}

	return "", fmt.Errorf("exceeded max steps (%d)", a.MaxSteps)
}
