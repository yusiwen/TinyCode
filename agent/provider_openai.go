package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// Bounds on one OpenAI-compatible request.
//
// The two measure different things, for the same reason the Ollama pair do (see
// provider_ollama.go): a batch answer has no intermediate progress to watch, so
// the whole request is bounded; a streaming answer may legitimately run for
// minutes while tokens keep arriving, so only the silence between them is
// bounded — a total limit would kill a long generation that is working fine.
// Both are variables so the tests can shrink them.
var (
	// openAIRequestTimeout bounds one non-streaming request, body included.
	openAIRequestTimeout = 120 * time.Second
	// openAIIdleTimeout bounds the wait for the next line of a streaming
	// response; every line the scanner delivers resets it.
	openAIIdleTimeout = 2 * time.Minute
)

// errOpenAISilent is the cancel cause of a streaming request that produced no
// data for openAIIdleTimeout, so the failure names the bound instead of the
// opaque transport error a cancelled context produces.
var errOpenAISilent = errors.New("stream went silent")

// openAIRequestError prefers the bound that fired over the transport error it
// produced: a cancelled request surfaces as "context canceled" or a closed
// connection, neither of which says why.
//
// A cancellation by the caller (Ctrl+C) keeps both, because the caller
// classifies its own cancellation with errors.Is(err, context.Canceled) — a TUI
// run shows "Interrupted" that way — while the transport error still explains
// what the read saw.
func openAIRequestError(ctx context.Context, err error) error {
	cause := context.Cause(ctx)
	if cause == nil {
		return err
	}
	if errors.Is(cause, context.Canceled) {
		return fmt.Errorf("%w: %w", cause, err)
	}
	return cause
}

// OpenAIProvider implements LLMProvider for OpenAI-compatible APIs (DeepSeek, OpenAI, Groq, etc.).
type OpenAIProvider struct {
	client  *openai.Client
	model   string
	apiKey  string
	baseURL string

	// route is the name the user gave this provider in config.json, and
	// costCurrency is the unit its route bills a reported cost in. Both are set
	// once from the configuration (SetRouteInfo) and are empty for a provider
	// built without one.
	route        string
	costCurrency string
}

// RouteInfo is implemented by a provider that can say which configuration route
// it serves and which model it uses by default.
//
// The agent needs both to look a price up, and only the provider knows them: a
// route can be switched while a session is running, so a value captured when the
// agent was built would go stale and price a call against another route's rates.
type RouteInfo interface {
	Route() string
	DefaultModel() string
	CostCurrency() string
}

// SetRouteInfo names the configuration route this provider serves, its default
// model and the unit that route bills a reported cost in.
func (p *OpenAIProvider) SetRouteInfo(route, model, costCurrency string) {
	p.route = route
	if model != "" {
		p.model = model
	}
	p.costCurrency = costCurrency
}

// Route returns the configuration name the user gave this provider.
func (p *OpenAIProvider) Route() string { return p.route }

// DefaultModel returns the model this provider uses when a request names none.
func (p *OpenAIProvider) DefaultModel() string { return p.model }

// CostCurrency returns the unit this route bills a reported cost in.
func (p *OpenAIProvider) CostCurrency() string { return p.costCurrency }

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
		Role             string            `json:"role"`
		Content          string            `json:"content"`
		Name             string            `json:"name,omitempty"`
		ToolCallID       string            `json:"tool_call_id,omitempty"`
		ToolCalls        []openai.ToolCall `json:"tool_calls,omitempty"`
		ReasoningContent string            `json:"reasoning_content,omitempty"`
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

	tlog.Trace("llm.provider", "request", "model", model, "body", string(body))

	// Bound the stream (issue #176). The cancel cause carries the bound that
	// fired into the error message; a streaming request starts its clock at the
	// request, so an endpoint that never sends even the response headers fails on
	// the same idle bound as one that goes quiet mid-answer. The batch path keeps
	// the whole-request bound on the client, because it has no interim progress
	// to watch.
	reqCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var watchdog *time.Timer
	if cb != nil {
		watchdog = time.AfterFunc(openAIIdleTimeout, func() {
			cancel(fmt.Errorf("%w for %s", errOpenAISilent, openAIIdleTimeout))
		})
		defer watchdog.Stop()
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		p.baseURL+"/chat/completions",
		bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	authVal := fmt.Sprintf("Bearer %s", p.apiKey)
	httpReq.Header.Set("Authorization", authVal)

	client := &http.Client{Timeout: openAIRequestTimeout}
	httpResp, err := client.Do(httpReq)
	start := time.Now()
	if err != nil {
		tlog.Error("llm.provider", "api error", "error", err)
		return nil, fmt.Errorf("api call: %w", openAIRequestError(reqCtx, err))
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != 200 {
		respBody, _ := io.ReadAll(httpResp.Body)
		tlog.Error("llm.provider", "api error", "status", httpResp.StatusCode)
		return nil, fmt.Errorf("openai api: status %d: %s", httpResp.StatusCode, string(respBody))
	}

	// Branch: streaming SSE or batch
	if cb != nil {
		return p.chatStream(reqCtx, httpResp.Body, start, cb, watchdog)
	}
	return p.chatBatch(reqCtx, httpResp.Body, start)
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
				Role             string            `json:"role"`
				Content          string            `json:"content"`
				ToolCalls        []openai.ToolCall `json:"tool_calls,omitempty"`
				ReasoningContent string            `json:"reasoning_content,omitempty"`
			} `json:"message"`
		} `json:"choices"`
		Usage *usageJSON `json:"usage,omitempty"`
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
		Usage:            rawResp.Usage.toUsage(),
		Cost:             rawResp.Usage.toCost(p.costCurrency),
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
//
// watchdog is the caller's idle bound for this stream (nil for none, as the
// parser-level tests use): every line the scanner delivers resets it, so it
// fires only when the endpoint stops talking. A read that fails — including one
// this bound cut — is returned as an error rather than as the partial answer it
// managed to send, so a stalled stream cannot be mistaken for a finished one.
func (p *OpenAIProvider) chatStream(ctx context.Context, body io.ReadCloser, start time.Time, cb *types.StreamCallbacks, watchdog *time.Timer) (*types.ChatResponse, error) {
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
		if watchdog != nil {
			watchdog.Reset(openAIIdleTimeout)
		}
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
			} `json:"choices"`
			Usage *usageJSON `json:"usage,omitempty"`
		}

		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			tlog.Debug("llm.provider", "sse_parse_error", "line", line, "error", err.Error())
			continue
		}

		// Usage arrives in a chunk of its own, after the one carrying
		// finish_reason and before [DONE]; that chunk has an empty choices
		// array, so it has to be read before the choices check below.
		if event.Usage != nil {
			result.Usage = event.Usage.toUsage()
			result.Cost = event.Usage.toCost(p.costCurrency)
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
	}

	if err := scanner.Err(); err != nil {
		tlog.Warn("llm.provider", "sse_scan_error", "error", err.Error())
		return nil, fmt.Errorf("openai stream: %w", openAIRequestError(ctx, err))
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

// usageJSON mirrors the OpenAI usage object, which the batch body and the final
// streaming chunk both carry. It is a pointer at every call site: an absent
// object means the endpoint reported nothing, which is not the same as zero.
//
// The wire names differ per route while the meaning does not, so this type is
// the only place that knows them: prompt_tokens_details.cached_tokens (OpenAI,
// OpenRouter, DeepSeek's newer shape) and prompt_cache_hit_tokens (DeepSeek's
// older one) are the same lane, and both end up in Usage.CachedPromptTokens.
type usageJSON struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	PromptTokensDetails *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`

	// DeepSeek's own names for the input split. They are read only when the
	// nested shape above is absent, so a route that sends both is not
	// double-counted.
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens,omitempty"`

	// Reported cost (OpenRouter). Its unit is the account's, not a currency code,
	// which is why it is carried through verbatim rather than converted.
	Cost       *float64 `json:"cost,omitempty"`
	CostDetail *struct {
		UpstreamInferenceCost *float64 `json:"upstream_inference_cost,omitempty"`
	} `json:"cost_details,omitempty"`
}

// toUsage normalizes a reported usage object for the shared types.Usage. A
// compatible endpoint may omit total_tokens, so the sum is derived from the two
// halves when it is absent; a genuinely zero report stays zero.
func (u *usageJSON) toUsage() *types.Usage {
	if u == nil {
		return nil
	}
	total := u.TotalTokens
	if total == 0 {
		total = u.PromptTokens + u.CompletionTokens
	}
	usage := &types.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      total,
	}
	if d := u.PromptTokensDetails; d != nil {
		usage.CachedPromptTokens = d.CachedTokens
		usage.CacheWriteTokens = d.CacheWriteTokens
	} else {
		usage.CachedPromptTokens = u.PromptCacheHitTokens
	}
	// prompt_cache_miss_tokens is the rest of the prompt, so it is not a field of
	// its own: deriving it from the hit count keeps one number per lane.
	if d := u.CompletionTokensDetails; d != nil {
		usage.ReasoningTokens = d.ReasoningTokens
	}
	return usage
}

// toCost reports the charge a route stated for this request, or nil when it
// stated none. The upstream figure is a detail of the charge, not a second one,
// so it is only used when the route billed nothing itself (a BYOK request, where
// the account is charged nothing and the upstream cost is the informative one).
func (u *usageJSON) toCost(currency string) *types.Cost {
	if u == nil {
		return nil
	}
	if u.Cost != nil {
		return &types.Cost{Amount: *u.Cost, Currency: currency}
	}
	if u.CostDetail != nil && u.CostDetail.UpstreamInferenceCost != nil {
		return &types.Cost{Amount: *u.CostDetail.UpstreamInferenceCost, Currency: currency}
	}
	return nil
}
