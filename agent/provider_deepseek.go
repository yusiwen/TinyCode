package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)
// DeepSeekProvider implements LLMProvider for DeepSeek (OpenAI-compatible API).
type DeepSeekProvider struct {
	client *openai.Client
	model  string
	apiKey string
	baseURL string
}

// NewDeepSeekProvider creates a provider using the given API key and base URL.
func NewDeepSeekProvider(apiKey, baseURL, model string) *DeepSeekProvider {
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = baseURL
	return &DeepSeekProvider{
		client:  openai.NewClientWithConfig(config),
		model:   model,
		apiKey:  apiKey,
		baseURL: baseURL,
	}
}

func (p *DeepSeekProvider) Name() string {
	return fmt.Sprintf("deepseek/%s", p.model)
}

func (p *DeepSeekProvider) Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	// Build messages with reasoning_content support (DeepSeek thinking mode).
	// go-openai's ChatCompletionMessage doesn't have reasoning_content, so we
	// use custom serialization via a raw-message struct.

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

	// Build request body manually for reasoning_content support
	bodyMap := map[string]any{
		"model":      p.model,
		"messages":   rawMsgs,
		"max_tokens": req.MaxTokens,
	}
	if len(openaiTools) > 0 {
		bodyMap["tools"] = openaiTools
	}

	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

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

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if httpResp.StatusCode != 200 {
		tlog.Error("llm.provider", "api error", "status", httpResp.StatusCode)
		return nil, fmt.Errorf("deepseek api: status %d: %s", httpResp.StatusCode, string(respBody))
	}

	// Parse response, extracting reasoning_content
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
		Content: choice.Content,
	}

	// Capture reasoning_content for thinking mode
	result.ReasoningContent = choice.ReasoningContent

	if len(choice.ToolCalls) > 0 {
		result.ToolCalls = make([]types.ToolCall, len(choice.ToolCalls))
		for i, tc := range choice.ToolCalls {
			result.ToolCalls[i] = types.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			}
		}
		tlog.Debug("llm.provider", "response", "model", p.model, "tool_calls", len(result.ToolCalls), "duration", time.Since(start).Round(time.Millisecond).String())
	} else {
		tlog.Debug("llm.provider", "response", "model", p.model, "content_len", len(result.Content), "duration", time.Since(start).Round(time.Millisecond).String())
	}

	return result, nil
}
