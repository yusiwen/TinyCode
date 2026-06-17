package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// Agent is the core ReAct loop.
type Agent struct {
	Config *AgentConfig // agent mode config (plan/build/subagent)

	Provider   LLMProvider
	Tools      []Tool
	Memory     types.MemoryStore
	MemoryMode int // 0=none, 1=auto, 2=on-demand
	// Session persistence
	SessionStore interface {
		Append(msg types.Message) error
		Flush() error
	}
	History []types.Message // multi-turn conversation history

	// Compression settings
	CompressionThreshold int // token threshold to trigger compression (0 = disabled)
	ContextLength        int // model context window size (0 = unknown)

	// Discovered context length (lowered after context_length_exceeded errors)
	discoveredCtxLen int

	// TodoStorer (used by compression for active-todo injection)
	TodoStorer interface{ FormatForInjection() string }

	SystemPrompt string
	MaxSteps     int
	MaxTokens    int
	Verbose      bool // when true, print detailed tool results
	ShowThinking bool // when true, display reasoning_content from thinking mode
	StreamCallbacks *types.StreamCallbacks // optional streaming callbacks (TUI mode)

	ContentStreamed bool // true when content was streamed via SSE; skip glamour re-print
}

// ANSI color codes for terminal output.
const (
	colorCyan    = "\033[36m"
	colorGray    = "\033[90m"
	colorYellow  = "\033[33m"
	colorDim     = "\033[2m"
	colorReset   = "\033[0m"

	thinkingPrefix = "| "
)

// agentPrefix returns the display prefix based on current mode config.
func (a *Agent) agentPrefix() string {
	if a.Config != nil {
		return "[" + a.Config.Name + "]"
	}
	return "[tinycode]"
}

// stepName prints the step header (always visible) in cyan.
// In TUI mode (StreamCallbacks set), this is handled by the TUI renderer.
func (a *Agent) stepName(format string, args ...any) {
	if a.StreamCallbacks != nil {
		return // TUI mode — direct stdout bypasses Bubble Tea
	}
	fmt.Print("\n" + colorCyan + a.agentPrefix() + " " + fmt.Sprintf(format, args...) + colorReset + "\n")
}

// stepDetail prints detailed output in gray, only when Verbose is enabled.
func (a *Agent) stepDetail(format string, args ...any) {
	if a.StreamCallbacks != nil {
		return
	}
	if a.Verbose {
		fmt.Print(colorGray + a.agentPrefix() + " " + fmt.Sprintf(format, args...) + colorReset + "\n")
	}
}

// showThinking prints the model's reasoning content in dim yellow with | prefix.
// Only shown when ShowThinking is enabled and reasoning_content is non-empty.
func (a *Agent) showThinking(reasoning string) {
	if !a.ShowThinking || reasoning == "" {
		return
	}
	for _, line := range strings.Split(strings.TrimRight(reasoning, "\n"), "\n") {
		fmt.Print(colorDim + colorYellow + "| " + line + colorReset + "\n")
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
			"Use tools when needed to accomplish the user's request. " +
			"Think step by step. You have a limited budget of 20 tool calls " +
			"per request — plan which files to read strategically. " +
			"Use bash (tree/find) to explore project structure first, " +
			"then read only the key files needed to answer.",
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
	// Resolve system prompt from config, falling back to Agent.SystemPrompt
	sysPrompt := a.SystemPrompt
	if a.Config != nil && a.Config.SystemPrompt != "" {
		sysPrompt = a.Config.SystemPrompt
	}
	messages := []types.Message{
		{Role: types.RoleSystem, Content: sysPrompt},
	}

	// Load multi-turn history, skipping messages that would cause API errors
	// Compress history if it exceeds the threshold
	compressed, err := a.compressHistory(a.History)
	if err == nil && compressed != nil {
		a.History = compressed
	}
	for _, msg := range a.History {
		// Skip assistant messages with neither content nor tool_calls
		if msg.Role == types.RoleAssistant && msg.Content == "" && len(msg.ToolCalls) == 0 {
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
	// Resolve max steps from config, falling back to Agent.MaxSteps
	maxSteps := a.MaxSteps
	if a.Config != nil && a.Config.MaxSteps > 0 {
		maxSteps = a.Config.MaxSteps
	}

	// Set plan mode write restriction
	if a.Config != nil && a.Config.Name == "plan" {
		types.PlanModeWriteRestricted = true
	} else {
		types.PlanModeWriteRestricted = false
	}

	for step < maxSteps {
		tlog.Info("agent.loop", "llm call", "step", step, "mode", a.agentPrefix())

		// Build tool definitions, filtering by config permissions
		toolDefs := make([]types.ToolDef, 0, len(a.Tools))
		for _, t := range a.Tools {
			if a.Config != nil && !ToolAllowedFor(a.Config, t.Name) {
				continue // skip tools not allowed in current mode
			}
			toolDefs = append(toolDefs, types.ToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			})
		}
					// Determine streaming callbacks: use Agent-level if set (TUI mode),
					// otherwise create default callbacks for terminal display.
					callbacks := a.StreamCallbacks
					tlog.Debug("agent.loop", "callbacks_check", "step", step, "has_callbacks", callbacks != nil)
					if callbacks == nil {
						var reasoningFirstToken bool
						callbacks = &types.StreamCallbacks{
							OnReasoningDelta: func(text string) {
								if a.ShowThinking {
									if !reasoningFirstToken {
										reasoningFirstToken = true
										fmt.Print(colorDim + colorYellow + thinkingPrefix)
									}
									fmt.Print(text)
								}
							},
							OnTextDelta: func(text string) {
								fmt.Print(colorReset + text)
							},
						}
					}

								// Call provider
								resp, err := a.Provider.Chat(ctx, types.ChatRequest{
						Messages:  messages,
						Tools:     toolDefs,
						MaxTokens: a.MaxTokens,
						StreamCallbacks: callbacks,
					})
		if err != nil {
			tlog.Error("agent.loop", "llm error", "step", step, "error", err)
			a.HandleContextError(err)
			return "", fmt.Errorf("LLM call failed: %w", err)
		}

		// Reasoning already handled by streaming callback (OnReasoningDelta)
		if a.ShowThinking {
			fmt.Print(colorReset)
		}

		// No tool calls → final answer
		if len(resp.ToolCalls) == 0 {
			// Degenerate case: empty content with no tool calls
			// LLM spent all tokens on reasoning and produced nothing.
			if resp.Content == "" {
				tlog.Warn("agent.loop", "empty_response", "step", step, "mode", a.agentPrefix(), "reasoning_len", len(resp.ReasoningContent))
				// Continue the loop to retry rather than returning empty
				messages = append(messages, types.Message{
					Role:    types.RoleAssistant,
					Content: "(model produced no output after thinking)",
				})
				step++
				continue
			}

			tlog.Info("agent.loop", "answer", "step", step, "mode", a.agentPrefix(), "resp_len", len(resp.Content))
			a.ContentStreamed = true
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

		// Multiple tool calls in one step
		toolCalls := resp.ToolCalls
		tlog.Debug("agent.loop", "tool calls", "step", step, "count", len(toolCalls))

		// Build tool names string for step header
		names := make([]string, len(toolCalls))
		for i, tc := range toolCalls {
			names[i] = tc.Name
		}
		a.stepName("[step %d] calling tools: %s", step, strings.Join(names, ", "))

		// Append assistant message with ALL tool calls
		assistantMsg := types.Message{
			Role:             types.RoleAssistant,
			Content:          "",
			ReasoningContent: resp.ReasoningContent,
		}
		assistantMsg.ToolCalls = make([]types.ToolCall, len(toolCalls))
		for i, tc := range toolCalls {
			assistantMsg.ToolCalls[i] = types.ToolCall{
				ID:        tc.ID,
				Name:      tc.Name,
				Arguments: tc.Arguments,
			}
		}
		messages = append(messages, assistantMsg)

		// Execute each tool call and collect results
		for _, tc := range toolCalls {
			tlog.Debug("agent.loop", "tool_check", "step", step, "tool", tc.Name, "has_callbacks", callbacks != nil, "has_ontoolcall", callbacks != nil && callbacks.OnToolCall != nil)
			if callbacks != nil && callbacks.OnToolCall != nil {
				// Extract a short argument summary for display
				argSummary := ""
				if tc.Arguments != "" {
					var raw map[string]any
					if err := json.Unmarshal([]byte(tc.Arguments), &raw); err == nil {
						// Use first string value as summary (e.g. file path, query)
						for _, v := range raw {
							if s, ok := v.(string); ok {
								argSummary = s
								break
							}
						}
					}
					if argSummary == "" && len(tc.Arguments) > 60 {
						argSummary = tc.Arguments[:60] + "..."
					}
				}
				callbacks.OnToolCall(tc.Name, argSummary)
			}
			tlog.Info("agent.loop", "tool exec", "step", step, "tool", tc.Name)
			var result string
			found := false
			for _, t := range a.Tools {
				if t.Name == tc.Name {
					// Runtime permission check (defense-in-depth)
					if a.Config != nil && !ToolAllowedFor(a.Config, t.Name) {
						result = fmt.Sprintf("[DENIED] %s is not available in %s mode.",
							t.Name, a.Config.Name)
						found = true
						break
					}
					found = true
					var args map[string]any
					if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
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
				result = fmt.Sprintf("unknown tool: %s", tc.Name)
			}

			// Show tool input and result (verbose only)
			if len(tc.Arguments) > 500 {
				a.stepDetail("[step %d] tool input (%s):\n%s...", step, tc.Name, tc.Arguments[:500])
			} else {
				a.stepDetail("[step %d] tool input (%s):\n%s", step, tc.Name, tc.Arguments)
			}
			if len(result) > 500 {
				a.stepDetail("[step %d] tool result (%s, %d chars):\n%s...", step, tc.Name, len(result), result[:500])
			} else {
				a.stepDetail("[step %d] tool result (%s, %d chars):\n%s", step, tc.Name, len(result), result)
			}

			if callbacks != nil && callbacks.OnToolResult != nil {
				callbacks.OnToolResult(tc.Name)
			}

			// Security intercept
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

			// Truncate large tool output
			trunc := TruncateOutput(result)
			truncatedResult := trunc.Content

			tlog.Debug("agent.loop", "tool result", "step", step, "tool", tc.Name, "size", len(result), "truncated", trunc.FullPath != "")

			messages = append(messages, types.Message{
				Role:        types.RoleTool,
				Content:     truncatedResult,
				Name:        tc.Name,
				ToolCallID:  tc.ID,
			})
		}
		step++
	}

	// Max steps reached: inject forced summary
	tlog.Warn("agent.loop", "max steps", "steps", maxSteps)
	messages = append(messages, types.Message{
		Role:    types.RoleUser,
		Content: fmt.Sprintf("You have reached the maximum step limit (%d steps). No more tool calls are allowed. Please summarize what you have accomplished so far and what remains to be done.", maxSteps),
	})
	// Force one more LLM call with no tools available
	resp, err := a.Provider.Chat(ctx, types.ChatRequest{
		Messages:  messages,
		Tools:     nil, // no tools — LLM must output text only
		MaxTokens: a.MaxTokens,
		StreamCallbacks: &types.StreamCallbacks{
			OnReasoningDelta: func(text string) {},
			OnTextDelta: func(text string) {
				if a.StreamCallbacks != nil && a.StreamCallbacks.OnTextDelta != nil {
					a.StreamCallbacks.OnTextDelta(text)
				} else {
					fmt.Print(text)
				}
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("step limit summary failed: %w", err)
	}
	return resp.Content, nil
}

// CompressHistory compresses a.History in-place using the agent's provider
// for summarization. Returns true if compression was applied.
func (a *Agent) CompressHistory() bool {
	before := len(a.History)
	compressed, err := a.compressHistory(a.History)
	if err != nil || compressed == nil {
		return false
	}
	a.History = compressed
	return len(a.History) < before
}
