package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)
// OpenAIProvider implements LLMProvider for OpenAI-compatible APIs (DeepSeek, OpenAI, Groq, etc.).
type OpenAIProvider struct {
	client *openai.Client
	model  string
	apiKey string
	baseURL string
}

// NewOpenAIProvider creates a provider for OpenAI-compatible APIs (DeepSeek, OpenAI, etc.).
func NewOpenAIProvider(apiKey, baseURL, model string) *OpenAIProvider {
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = baseURL
	return &OpenAIProvider{
		client:  openai.NewClientWithConfig(config),
		model:   model,
		apiKey:  apiKey,
		baseURL: baseURL,
	}
}

func (p *OpenAIProvider) Name() string {
	return fmt.Sprintf("openai/%s", p.model)
}

func (p *OpenAIProvider) Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	// Build messages with reasoning_content support (DeepSeek thinking mode).
	type rawMsg struct {
		Role             string             `json:"role"`
		Content          string             `json:"content"`
		Name             string             `json:"name,omitempty"`
		ToolCallID       string             `json:"tool_call_id,omitempty"`
		ToolCalls        []openai.ToolCall  `json:"tool_calls,omitempty"`
		ReasoningContent string             `json:"reasoning_content,omitempty"`
	}

	rawMsgs := make([]rawMsg, len(req.Messages))
	for i, msg := range req.Messages {
		m := rawMsg{
			Role:             msg.Role,
			Content:          msg.Content,
			Name:             msg.Name,
			ToolCallID:       msg.ToolCallID,
			ReasoningContent: msg.ReasoningContent,
		}

		if len(msg.ToolCalls) > 0 {
			tcs := make([]openai.ToolCall, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				tcs[j] = openai.ToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: openai.FunctionCall{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				}
			}
			m.ToolCalls = tcs
		}

		rawMsgs[i] = m
	}

	// Convert types.ToolDef → openai.Tool
	openaiTools := make([]openai.Tool, len(req.Tools))
	for i, td := range req.Tools {
		openaiTools[i] = openai.Tool{
			Type: "function",
			Function: &openai.FunctionDefinition{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  td.Parameters,
			},
		}
	}

	// Build request body
	model := p.model
	if req.Model != "" {
		model = req.Model
	}
	bodyMap := map[string]any{
		"model":      model,
		"messages":   rawMsgs,
		"max_tokens": req.MaxTokens,
	}
	if len(openaiTools) > 0 {
		bodyMap["tools"] = openaiTools
	}

	// Use streaming if callbacks are provided
	cb := req.StreamCallbacks
	if cb != nil {
		bodyMap["stream"] = true
		bodyMap["stream_options"] = map[string]any{"include_usage": true}
	}

	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	tlog.Trace("llm.provider", "request", "model", p.model, "body", string(body))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions",
		bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	authVal := fmt.Sprintf("Bearer %s", p.apiKey)
	httpReq.Header.Set("Authorization", authVal)

	client := &http.Client{Timeout: 120 * time.Second}
	httpResp, err := client.Do(httpReq)
	start := time.Now()
	if err != nil {
		tlog.Error("llm.provider", "api error", "error", err)
		return nil, fmt.Errorf("api call: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != 200 {
		respBody, _ := io.ReadAll(httpResp.Body)
		tlog.Error("llm.provider", "api error", "status", httpResp.StatusCode)
		return nil, fmt.Errorf("openai api: status %d: %s", httpResp.StatusCode, string(respBody))
	}

	// Branch: streaming SSE or batch
	if cb != nil {
		return p.chatStream(ctx, httpResp.Body, start, cb)
	}
	return p.chatBatch(ctx, httpResp.Body, start)
}

// chatBatch parses a batch (non-streaming) response.
func (p *OpenAIProvider) chatBatch(ctx context.Context, body io.ReadCloser, start time.Time) (*types.ChatResponse, error) {
	respBody, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	tlog.Trace("llm.provider", "response", "model", p.model, "body", string(respBody))

	var rawResp struct {
		Choices []struct {
			Message struct {
				Role             string             `json:"role"`
				Content          string             `json:"content"`
				ToolCalls        []openai.ToolCall  `json:"tool_calls,omitempty"`
				ReasoningContent string             `json:"reasoning_content,omitempty"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(respBody, &rawResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if len(rawResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices returned")
	}

	choice := rawResp.Choices[0].Message
	result := &types.ChatResponse{
		Content:          choice.Content,
		ReasoningContent: choice.ReasoningContent,
	}

	if len(choice.ToolCalls) > 0 {
		result.ToolCalls = make([]types.ToolCall, len(choice.ToolCalls))
		for i, tc := range choice.ToolCalls {
			result.ToolCalls[i] = types.ToolCall{
				ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
			}
		}
		tlog.Debug("llm.provider", "response", "model", p.model, "tool_calls", len(result.ToolCalls), "duration", time.Since(start).Round(time.Millisecond).String())
	} else {
		tlog.Debug("llm.provider", "response", "model", p.model, "content_len", len(result.Content), "duration", time.Since(start).Round(time.Millisecond).String())
	}
	return result, nil
}

// chatStream parses an SSE streaming response with real-time callbacks.
func (p *OpenAIProvider) chatStream(ctx context.Context, body io.ReadCloser, start time.Time, cb *types.StreamCallbacks) (*types.ChatResponse, error) {
	defer body.Close()

	result := &types.ChatResponse{}
	var reasoning strings.Builder
	var content strings.Builder

	// Tool call accumulation: index → {id, name, arguments}
	type streamTool struct {
		id   string
		name string
		args strings.Builder
	}
	toolByIndex := map[int]*streamTool{}
	toolIndices := []int{} // insertion order

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	reasoningWritten := false

	for scanner.Scan() {
		line := scanner.Text()

		// SSE format: "data: {...}" or "data: [DONE]"
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")

		// Stream end marker
		if payload == "[DONE]" {
			break
		}

		// Parse the SSE event JSON
		var event struct {
			Choices []struct {
				Delta struct {
					Role             string            `json:"role,omitempty"`
					Content          string            `json:"content,omitempty"`
					ReasoningContent string            `json:"reasoning_content,omitempty"`
					ToolCalls        []jsonToolCallRef `json:"tool_calls,omitempty"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason,omitempty"`
			} `json:"choices"`
		}

		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			tlog.Debug("llm.provider", "sse_parse_error", "line", line, "error", err.Error())
			continue
		}

		if len(event.Choices) == 0 {
			continue
		}

		delta := event.Choices[0].Delta

		// Reasoning content delta
		if delta.ReasoningContent != "" {
			if !reasoningWritten {
				reasoningWritten = true
			}
			reasoning.WriteString(delta.ReasoningContent)
			if cb.OnReasoningDelta != nil {
				cb.OnReasoningDelta(delta.ReasoningContent)
			}
		}

		// Text content delta
		if delta.Content != "" {
			content.WriteString(delta.Content)
			if cb.OnTextDelta != nil {
				cb.OnTextDelta(delta.Content)
			}
		}

		// Tool call deltas — by index
		for _, tc := range delta.ToolCalls {
			existing, ok := toolByIndex[tc.Index]
			if !ok {
				// First event for this tool call: has id + name
				existing = &streamTool{
					id:   tc.ID,
					name: tc.Function.Name,
				}
				toolByIndex[tc.Index] = existing
				toolIndices = append(toolIndices, tc.Index)
			}
			if tc.Function.Arguments != "" {
				existing.args.WriteString(tc.Function.Arguments)
			}
			if tc.ID != "" {
				existing.id = tc.ID
			}
		}

		// Finish reason — last event before [DONE]
		if event.Choices[0].FinishReason != "" {
			// Signal end of real-time output if content was streamed
			break
		}
	}

	if err := scanner.Err(); err != nil {
		tlog.Warn("llm.provider", "sse_scan_error", "error", err.Error())
	}

	result.ReasoningContent = reasoning.String()
	result.Content = content.String()

	// Build tool calls in insertion order
	if len(toolIndices) > 0 {
		result.ToolCalls = make([]types.ToolCall, len(toolIndices))
		for i, idx := range toolIndices {
			t := toolByIndex[idx]
			result.ToolCalls[i] = types.ToolCall{
				ID: t.id, Name: t.name, Arguments: t.args.String(),
			}
		}
		tlog.Debug("llm.provider", "response", "model", p.model, "tool_calls", len(result.ToolCalls), "duration", time.Since(start).Round(time.Millisecond).String())
	} else {
		tlog.Debug("llm.provider", "response", "model", p.model, "content_len", len(result.Content), "duration", time.Since(start).Round(time.Millisecond).String())
	}
	return result, nil
}

// jsonToolCallRef mirrors the streaming tool call delta JSON structure.
type jsonToolCallRef struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}
