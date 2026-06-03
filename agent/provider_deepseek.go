package agent

import (
	"context"
	"fmt"

	"github.com/sashabaranov/go-openai"
	"github.com/yusiwen/tinycode/types"
)

// DeepSeekProvider implements LLMProvider for DeepSeek (OpenAI-compatible API).
type DeepSeekProvider struct {
	client *openai.Client
	model  string
}

// NewDeepSeekProvider creates a provider using the given API key and base URL.
func NewDeepSeekProvider(apiKey, baseURL, model string) *DeepSeekProvider {
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = baseURL
	return &DeepSeekProvider{
		client: openai.NewClientWithConfig(config),
		model:  model,
	}
}

func (p *DeepSeekProvider) Name() string {
	return fmt.Sprintf("deepseek/%s", p.model)
}

func (p *DeepSeekProvider) Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	// Convert types.Message → openai.ChatCompletionMessage
	openaiMsgs := make([]openai.ChatCompletionMessage, len(req.Messages))
	for i, msg := range req.Messages {
		m := openai.ChatCompletionMessage{
			Role:        msg.Role,
			Content:     msg.Content,
			Name:        msg.Name,
			ToolCallID:  msg.ToolCallID,
		}

		// Assistant tool-call messages: serialize ToolCall
		if msg.ToolCall != nil {
			m.ToolCalls = []openai.ToolCall{{
				ID:   msg.ToolCall.ID,
				Type: "function",
				Function: openai.FunctionCall{
					Name:      msg.ToolCall.Name,
					Arguments: msg.ToolCall.Arguments,
				},
			}}
		}

		openaiMsgs[i] = m
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

	apiReq := openai.ChatCompletionRequest{
		Model:    p.model,
		Messages: openaiMsgs,
		MaxTokens: req.MaxTokens,
	}

	if len(openaiTools) > 0 {
		apiReq.Tools = openaiTools
	}

	resp, err := p.client.CreateChatCompletion(ctx, apiReq)
	if err != nil {
		return nil, fmt.Errorf("deepseek api: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no choices returned")
	}

	choice := resp.Choices[0]
	result := &types.ChatResponse{
		Content: choice.Message.Content,
	}

	// Check for tool calls
	if len(choice.Message.ToolCalls) > 0 {
		tc := choice.Message.ToolCalls[0]
		result.ToolCall = &types.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		}
	}

	return result, nil
}
