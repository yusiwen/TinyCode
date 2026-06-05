//go:build !no_ollama

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

	"github.com/yusiwen/tinycode/tlog"
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

		if len(msg.ToolCalls) > 0 {
			tcs := make([]ollamaToolCall, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				tcs[j] = ollamaToolCall{
					Function: ollamaFunctionCall{
						Name:      tc.Name,
						Arguments: json.RawMessage(tc.Arguments),
					},
				}
			}
			m.ToolCalls = tcs
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
		Stream:   req.StreamCallbacks != nil,
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

	if req.StreamCallbacks != nil {
		return p.ollamaStream(ctx, httpResp.Body, req.StreamCallbacks)
	}

	return p.ollamaBatch(httpResp.Body)
}

// ollamaBatch parses a batch (non-streaming) Ollama response.
func (p *OllamaProvider) ollamaBatch(body io.ReadCloser) (*types.ChatResponse, error) {
	defer body.Close()

	var ollamaResp struct {
		Message struct {
			Role      string           `json:"role"`
			Content   string           `json:"content"`
			Thinking  string           `json:"thinking,omitempty"`
			ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
	}

	if err := json.NewDecoder(body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("ollama decode: %w", err)
	}

	result := &types.ChatResponse{
		Content:          ollamaResp.Message.Content,
		ReasoningContent: ollamaResp.Message.Thinking,
	}

	if len(ollamaResp.Message.ToolCalls) > 0 {
		result.ToolCalls = make([]types.ToolCall, len(ollamaResp.Message.ToolCalls))
		for i, tc := range ollamaResp.Message.ToolCalls {
			result.ToolCalls[i] = types.ToolCall{
				ID:        tc.Function.Name,
				Name:      tc.Function.Name,
				Arguments: string(tc.Function.Arguments),
			}
		}
	}

	return result, nil
}

// ollamaStream parses a streaming Ollama response with real-time callbacks.
// Each line is JSON: {"message":{"role":"assistant","content":"token"}}
// Final line: {"done":true}
func (p *OllamaProvider) ollamaStream(ctx context.Context, body io.ReadCloser, cb *types.StreamCallbacks) (*types.ChatResponse, error) {
	defer body.Close()

	result := &types.ChatResponse{}
	var thinking strings.Builder
	var content strings.Builder
	var toolCalls []ollamaToolCall

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Parse each line as a streaming response
		var event struct {
			Message struct {
				Role      string           `json:"role,omitempty"`
				Content   string           `json:"content,omitempty"`
				Thinking  string           `json:"thinking,omitempty"`
				ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
			} `json:"message,omitempty"`
			Done bool `json:"done,omitempty"`
		}

		if err := json.Unmarshal([]byte(line), &event); err != nil {
			tlog.Debug("ollama.stream", "parse_error", "line", line, "error", err.Error())
			continue
		}

		if event.Done {
			break
		}

		if event.Message.Thinking != "" {
			thinking.WriteString(event.Message.Thinking)
			if cb.OnReasoningDelta != nil {
				cb.OnReasoningDelta(event.Message.Thinking)
			}
		}

		if event.Message.Content != "" {
			content.WriteString(event.Message.Content)
			if cb.OnTextDelta != nil {
				cb.OnTextDelta(event.Message.Content)
			}
		}

		if len(event.Message.ToolCalls) > 0 {
			toolCalls = event.Message.ToolCalls
		}
	}

	if err := scanner.Err(); err != nil {
		tlog.Warn("ollama.stream", "scan_error", "error", err.Error())
	}

	result.Content = content.String()
	result.ReasoningContent = thinking.String()

	if len(toolCalls) > 0 {
		result.ToolCalls = make([]types.ToolCall, len(toolCalls))
		for i, tc := range toolCalls {
			result.ToolCalls[i] = types.ToolCall{
				ID:        tc.Function.Name,
				Name:      tc.Function.Name,
				Arguments: string(tc.Function.Arguments),
			}
		}
	}

	return result, nil
}
