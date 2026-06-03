//go:build !no_ollama

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/yusiwen/tinycode/types"
)

type OllamaProvider struct {
	baseURL string
	model   string
	client  *http.Client
}

func NewOllamaProvider(baseURL, model string) *OllamaProvider {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return &OllamaProvider{
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{},
	}
}

func (p *OllamaProvider) Name() string {
	return fmt.Sprintf("ollama/%s", p.model)
}

type ollamaMessage struct {
	Role      string            `json:"role"`
	Content   string            `json:"content"`
	ToolCalls []ollamaToolCall  `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaFunctionCall `json:"function"`
}

type ollamaFunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ollamaTool struct {
	Type     string            `json:"type"`
	Function ollamaFunctionDef `json:"function"`
}

type ollamaFunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Options  map[string]any  `json:"options"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
}

type ollamaChatResponse struct {
	Message ollamaMessage `json:"message"`
}

func (p *OllamaProvider) Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	ollamaMsgs := make([]ollamaMessage, len(req.Messages))
	for i, msg := range req.Messages {
		m := ollamaMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}

		switch msg.Role {
		case types.RoleTool:
			m.Role = types.RoleUser
			if msg.Name != "" {
				m.Content = fmt.Sprintf("Tool (%s): %s", msg.Name, msg.Content)
			}
		}

		if msg.ToolCall != nil {
			m.ToolCalls = []ollamaToolCall{{
				Function: ollamaFunctionCall{
					Name:      msg.ToolCall.Name,
					Arguments: json.RawMessage(msg.ToolCall.Arguments),
				},
			}}
		}

		ollamaMsgs[i] = m
	}

	ollamaTools := make([]ollamaTool, len(req.Tools))
	for i, td := range req.Tools {
		ollamaTools[i] = ollamaTool{
			Type: "function",
			Function: ollamaFunctionDef{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  td.Parameters,
			},
		}
	}

	apiReq := ollamaChatRequest{
		Model:    p.model,
		Messages: ollamaMsgs,
		Stream:   false,
		Options:  map[string]any{},
	}

	if len(ollamaTools) > 0 {
		apiReq.Tools = ollamaTools
	}

	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("ollama marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama api: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("ollama api: status %d: %s", httpResp.StatusCode, string(respBody))
	}

	var ollamaResp ollamaChatResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("ollama decode: %w", err)
	}

	result := &types.ChatResponse{
		Content: ollamaResp.Message.Content,
	}

	if len(ollamaResp.Message.ToolCalls) > 0 {
		tc := ollamaResp.Message.ToolCalls[0]
		result.ToolCall = &types.ToolCall{
			ID:        tc.Function.Name,
			Name:      tc.Function.Name,
			Arguments: string(tc.Function.Arguments),
		}
	}

	return result, nil
}
