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

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// Bounds on one native Anthropic request.
//
// They measure the same two things the OpenAI-compatible and Ollama providers
// measure: a batch answer has no intermediate progress to watch, so the whole
// request is bounded; a streaming answer may legitimately run for minutes, so
// only the silence between events is bounded. Both are variables so a test can
// shrink them rather than wait.
var (
	// anthropicRequestTimeout bounds one non-streaming request, body included.
	anthropicRequestTimeout = 120 * time.Second
	// anthropicIdleTimeout bounds the wait for the next SSE line.
	anthropicIdleTimeout = 2 * time.Minute
)

// anthropicVersion is the Messages API version this request shape was written
// against. It is a date, not a release number: Anthropic versions the wire
// contract, and a caller that omits the header is rejected.
const anthropicVersion = "2023-06-01"

// anthropicCacheTTLBeta is the beta header required for a one-hour cache
// lifetime. It is sent only when the route asked for "1h"; the five-minute
// default is generally available.
const anthropicCacheTTLBeta = "extended-cache-ttl-2025-04-11"

// AnthropicProvider implements LLMProvider against the native Messages API.
//
// It exists because the OpenAI-compatible shape loses two facts the native API
// reports: the reasoning stream is a first-class block type with a signature that
// must be replayed, and the cache counters are separate input lanes
// (cache_creation_input_tokens, cache_read_input_tokens) rather than a detail of
// one prompt count (roadmap A6).
type AnthropicProvider struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client

	// thinkingBudget is the extended-thinking budget in tokens. Zero sends no
	// thinking request, which is the vendor's default.
	thinkingBudget int

	// route and costCurrency name the configuration entry this provider serves,
	// the same way the other backends carry them (roadmap A5).
	route        string
	costCurrency string

	// cache is the route's prompt-cache policy (roadmap A4). Only "explicit"
	// places a breakpoint here; "auto" has no Anthropic equivalent and the
	// builder refuses it.
	cache PromptCachePolicy
}

// NewAnthropicProvider creates a native Messages API provider. An empty base URL
// means api.anthropic.com.
func NewAnthropicProvider(apiKey, baseURL, model string) *AnthropicProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.anthropic.com"
	}
	return &AnthropicProvider{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{},
	}
}

// SetThinkingBudget asks the route to reason before answering. The value is
// passed through unchanged: the vendor's minimum and maximum are model-specific,
// and clamping would spend money on a budget nobody chose.
func (p *AnthropicProvider) SetThinkingBudget(tokens int) { p.thinkingBudget = tokens }

// SetRouteInfo names the configuration route, its default model and the unit its
// reported charge is denominated in. Anthropic reports no charge, but the
// currency is kept for the shared signature and for a future compatible gateway.
func (p *AnthropicProvider) SetRouteInfo(route, model, costCurrency string) {
	p.route = route
	if model != "" {
		p.model = model
	}
	p.costCurrency = costCurrency
}

// Route returns the configuration name the user gave this provider.
func (p *AnthropicProvider) Route() string { return p.route }

// DefaultModel returns the model this provider uses when a request names none.
func (p *AnthropicProvider) DefaultModel() string { return p.model }

// CostCurrency returns the unit this route bills a reported charge in.
func (p *AnthropicProvider) CostCurrency() string { return p.costCurrency }

// SetPromptCache sets how this route is told to cache its prompt prefix.
func (p *AnthropicProvider) SetPromptCache(policy PromptCachePolicy) { p.cache = policy }

// Name names the backend and model for display.
func (p *AnthropicProvider) Name() string { return fmt.Sprintf("anthropic/%s", p.model) }

// endpoint resolves the Messages URL from the configured base. A base that
// already carries /v1 is accepted, because users paste it from the vendor's
// documentation, and appending another /v1 would produce /v1/v1/messages.
func (p *AnthropicProvider) endpoint() string {
	if strings.HasSuffix(p.baseURL, "/v1") {
		return p.baseURL + "/messages"
	}
	return p.baseURL + "/v1/messages"
}

// anthropicBlock is one content block in either direction.
//
// The fields are deliberately one struct rather than one type per block: the
// request and the response use the same tagged union, and splitting it would
// duplicate the JSON tags for the four block kinds this backend speaks.
type anthropicBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result. Content is a pointer so an empty tool result still sends the
	// required field: a command that printed nothing is a result, not an absent
	// block.
	ToolUseID string  `json:"tool_use_id,omitempty"`
	Content   *string `json:"content,omitempty"`
	IsError   bool    `json:"is_error,omitempty"`

	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`

	CacheControl map[string]any `json:"cache_control,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    []anthropicBlock   `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream,omitempty"`
	Thinking  *anthropicThinking `json:"thinking,omitempty"`
}

// anthropicUsage keeps every counter a pointer: an absent lane must not read as
// zero, because the total prompt count is built by adding lanes that the vendor
// may not all have reported.
type anthropicUsage struct {
	InputTokens              *int `json:"input_tokens,omitempty"`
	OutputTokens             *int `json:"output_tokens,omitempty"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens,omitempty"`
}

func (u *anthropicUsage) toUsage() *types.Usage {
	if u == nil {
		return nil
	}
	if u.InputTokens == nil && u.OutputTokens == nil &&
		u.CacheCreationInputTokens == nil && u.CacheReadInputTokens == nil {
		return nil
	}
	var input, output, written, cached int
	if u.InputTokens != nil {
		input = *u.InputTokens
	}
	if u.OutputTokens != nil {
		output = *u.OutputTokens
	}
	if u.CacheCreationInputTokens != nil {
		written = *u.CacheCreationInputTokens
	}
	if u.CacheReadInputTokens != nil {
		cached = *u.CacheReadInputTokens
	}
	// input_tokens counts only the uncached part of the prompt; the two cache
	// lanes are additional input the vendor billed differently. PromptTokens is
	// the whole prompt because the price table carves the lanes back out.
	prompt := input + written + cached
	return &types.Usage{
		PromptTokens:       prompt,
		CompletionTokens:   output,
		TotalTokens:        prompt + output,
		CachedPromptTokens: cached,
		CacheWriteTokens:   written,
	}
}

// Chat sends one Messages request and reads its answer.
func (p *AnthropicProvider) Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	body, err := p.requestBody(model, req)
	if err != nil {
		return nil, err
	}
	tlog.Trace("llm.provider", "request", "backend", "anthropic", "model", model, "body", string(body))

	// Retry a transient failure (issue #192), with the same safe-replay rule as
	// every other backend: a stream the caller has already seen is not repeated.
	maxAttempts := retryMaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	attempts := 0
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attempts = attempt
		tracker := trackedCallbacks(req.StreamCallbacks)
		resp, err := p.attempt(ctx, body, tracker.callbacks())
		if err == nil {
			resp.Route, resp.Model = p.route, model
			return resp, nil
		}
		lastErr = err
		if !shouldRetry(ctx, err, tracker.deliveredSomething(), attempt) {
			break
		}
		delay := backoffDelay(attempt, retryAfterOf(err))
		tlog.Warn("llm.provider", "retrying", "backend", "anthropic", "attempt", attempt+1,
			"of", maxAttempts, "in", delay.Round(time.Millisecond).String(), "error", err.Error())
		notifyRetry(req.StreamCallbacks, attempt+1, maxAttempts, err)
		if serr := sleepCtx(ctx, delay); serr != nil {
			return nil, fmt.Errorf("anthropic retry wait: %w", serr)
		}
	}
	return nil, attemptsError(attempts, lastErr)
}

// requestBody builds the JSON body, including the message and tool mapping.
func (p *AnthropicProvider) requestBody(model string, req types.ChatRequest) ([]byte, error) {
	system, messages := anthropicMessages(req.Messages)

	if p.cache.PlacesBreakpoints() {
		if bp := cacheBreakpointIndex(req.Messages); bp >= 0 {
			// The breakpoint is the last leading system message. The system
			// slice has no direct index into the original history, so the same
			// content is located by its position among the system messages.
			if i := anthropicSystemIndex(req.Messages, bp); i >= 0 {
				system[i].CacheControl = p.cache.cacheControl()
			}
		}
	}

	tools := make([]anthropicTool, len(req.Tools))
	for i, td := range req.Tools {
		tools[i] = anthropicTool{
			Name:        td.Name,
			Description: td.Description,
			InputSchema: td.Parameters,
		}
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		// The Messages API requires the field. The agent sets a default, but a
		// caller that constructs its own request would otherwise be rejected for
		// a value the provider can supply.
		maxTokens = 4096
	}

	out := anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  messages,
		Tools:     tools,
	}
	if req.StreamCallbacks != nil {
		out.Stream = true
	}
	if p.thinkingBudget > 0 {
		out.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: p.thinkingBudget}
	}
	return json.Marshal(out)
}

// anthropicMessages maps the shared history onto the Messages API.
//
// The two shapes differ in three places: the system prompt is a top-level field
// rather than a message, a tool call is a tool_use block on the assistant turn,
// and a tool result is a tool_result block on the user turn. Consecutive tool
// messages — one per call in a step — are grouped into one user message, which is
// what the API requires: every tool_use must be answered in the immediately
// following user turn.
func anthropicMessages(msgs []types.Message) (system []anthropicBlock, messages []anthropicMessage) {
	for _, m := range msgs {
		if m.Role == types.RoleSystem && m.Content != "" {
			system = append(system, anthropicBlock{Type: "text", Text: m.Content})
		}
	}

	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case types.RoleSystem:
			continue

		case types.RoleTool:
			var results []anthropicBlock
			for ; i < len(msgs) && msgs[i].Role == types.RoleTool; i++ {
				t := msgs[i]
				id := t.ToolCallID
				if id == "" {
					// A backend without call ids (Ollama) answers by name, and
					// the history that reaches this point carries the name. It
					// is the best identity available.
					id = t.Name
				}
				content := t.Content
				results = append(results, anthropicBlock{
					Type:      "tool_result",
					ToolUseID: id,
					Content:   &content,
				})
			}
			i--
			messages = append(messages, anthropicMessage{Role: "user", Content: results})

		case types.RoleAssistant:
			var blocks []anthropicBlock
			// A thinking block has to come first and has to be byte-identical to
			// what the vendor sent, signature included. A response whose
			// reasoning the vendor did not sign is not replayed: an invented
			// signature is rejected, and dropping the text is better than
			// corrupting the turn.
			if m.ReasoningContent != "" && m.ReasoningSignature != "" {
				blocks = append(blocks, anthropicBlock{
					Type:      "thinking",
					Thinking:  m.ReasoningContent,
					Signature: m.ReasoningSignature,
				})
			}
			if m.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: toolInput(tc.Arguments),
				})
			}
			if len(blocks) > 0 {
				messages = append(messages, anthropicMessage{Role: "assistant", Content: blocks})
			}

		default: // user
			if m.Content == "" {
				continue
			}
			messages = append(messages, anthropicMessage{
				Role:    "user",
				Content: []anthropicBlock{{Type: "text", Text: m.Content}},
			})
		}
	}
	return system, messages
}

// anthropicSystemIndex maps a message index to its position among the system
// messages, or -1 when that message is not a system message.
func anthropicSystemIndex(msgs []types.Message, target int) int {
	pos := 0
	for i, m := range msgs {
		if m.Role != types.RoleSystem {
			continue
		}
		if i == target {
			return pos
		}
		pos++
	}
	return -1
}

// toolInput turns the raw argument text into the JSON object the API expects. An
// empty argument list is an empty object; unparseable text is passed through, so
// the API's own error names the malformed JSON instead of this code silently
// sending an empty call.
func toolInput(args string) json.RawMessage {
	if strings.TrimSpace(args) == "" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(args)
}

// attempt makes one HTTP request and reads its answer.
func (p *AnthropicProvider) attempt(ctx context.Context, body []byte, cb *types.StreamCallbacks) (*types.ChatResponse, error) {
	reqCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var watchdog *time.Timer
	if cb != nil {
		watchdog = time.AfterFunc(anthropicIdleTimeout, func() {
			cancel(fmt.Errorf("%w for %s", errStreamSilent, anthropicIdleTimeout))
		})
	} else {
		watchdog = time.AfterFunc(anthropicRequestTimeout, func() {
			cancel(fmt.Errorf("no answer within %s: %w", anthropicRequestTimeout, context.DeadlineExceeded))
		})
	}
	defer watchdog.Stop()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if p.cache.TTL == "1h" {
		httpReq.Header.Set("anthropic-beta", anthropicCacheTTLBeta)
	}

	httpResp, err := p.client.Do(httpReq)
	start := time.Now()
	if err != nil {
		tlog.Error("llm.provider", "api error", "backend", "anthropic", "error", err)
		return nil, fmt.Errorf("anthropic api: %w", contextRequestError(reqCtx, err))
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		tlog.Error("llm.provider", "api error", "backend", "anthropic", "status", httpResp.StatusCode)
		return nil, fmt.Errorf("anthropic api: %w", &statusError{
			status:     httpResp.StatusCode,
			body:       string(respBody),
			retryAfter: parseRetryAfter(httpResp.Header.Get("Retry-After")),
		})
	}

	if cb != nil {
		return p.chatStream(reqCtx, httpResp.Body, start, cb, watchdog)
	}
	return p.chatBatch(httpResp.Body, start)
}

// anthropicWireResponse is the batch response shape.
type anthropicWireResponse struct {
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      *anthropicUsage  `json:"usage"`
}

// chatBatch parses a non-streaming Messages response.
func (p *AnthropicProvider) chatBatch(body io.ReadCloser, start time.Time) (*types.ChatResponse, error) {
	defer body.Close()

	var wire anthropicWireResponse
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, fmt.Errorf("anthropic decode: %w", err)
	}
	if len(wire.Content) == 0 {
		return nil, fmt.Errorf("no content blocks returned")
	}

	result := responseFromBlocks(wire.Content)
	result.Usage = wire.Usage.toUsage()

	if len(result.ToolCalls) > 0 {
		tlog.Debug("llm.provider", "response", "backend", "anthropic", "model", p.model,
			"tool_calls", len(result.ToolCalls), "duration", time.Since(start).Round(time.Millisecond).String())
	} else {
		tlog.Debug("llm.provider", "response", "backend", "anthropic", "model", p.model,
			"content_len", len(result.Content), "duration", time.Since(start).Round(time.Millisecond).String())
	}
	return result, nil
}

// responseFromBlocks flattens the block list into the shared response. Text is
// concatenated, thinking text is concatenated, and every tool_use becomes one
// call in block order.
func responseFromBlocks(blocks []anthropicBlock) *types.ChatResponse {
	result := &types.ChatResponse{}
	var text, reasoning strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "thinking":
			reasoning.WriteString(b.Thinking)
			if b.Signature != "" {
				result.ReasoningSignature = b.Signature
			}
		case "tool_use":
			result.ToolCalls = append(result.ToolCalls, types.ToolCall{
				ID:        b.ID,
				Name:      b.Name,
				Arguments: string(b.Input),
			})
		}
	}
	result.Content = text.String()
	result.ReasoningContent = reasoning.String()
	return result
}

// anthropicEvent is one SSE event. Only the fields this backend consumes are
// decoded; the unused ones stay out of the way.
type anthropicEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage *anthropicUsage `json:"usage"`
	} `json:"message,omitempty"`
	ContentBlock *anthropicBlock `json:"content_block,omitempty"`
	Delta        *struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		Thinking    string `json:"thinking,omitempty"`
		Signature   string `json:"signature,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
		StopReason  string `json:"stop_reason,omitempty"`
	} `json:"delta,omitempty"`
	Usage *anthropicUsage `json:"usage,omitempty"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// chatStream reads the Messages SSE stream.
//
// The event names are part of the payload's own "type" field, so the parser reads
// data lines only: an event line would be a second, redundant source of truth.
// The final usage is assembled from message_start (input lanes) and message_delta
// (output lane), which is why both are kept in one accumulator.
func (p *AnthropicProvider) chatStream(ctx context.Context, body io.ReadCloser, start time.Time, cb *types.StreamCallbacks, watchdog *time.Timer) (*types.ChatResponse, error) {
	defer body.Close()

	result := &types.ChatResponse{}
	var text, reasoning, signature strings.Builder
	usage := &anthropicUsage{}
	usageSeen := false

	type streamTool struct {
		id   string
		name string
		args strings.Builder
	}
	toolsByIndex := map[int]*streamTool{}
	var toolOrder []int

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	for scanner.Scan() {
		if watchdog != nil {
			watchdog.Reset(anthropicIdleTimeout)
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}

		var event anthropicEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			tlog.Debug("llm.provider", "sse_parse_error", "backend", "anthropic", "error", err.Error())
			continue
		}

		if event.Type == "error" {
			if event.Error != nil {
				return nil, fmt.Errorf("anthropic stream: %s: %s", event.Error.Type, event.Error.Message)
			}
			return nil, fmt.Errorf("anthropic stream: unknown error event")
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil && event.Message.Usage != nil {
				mergeAnthropicUsage(usage, event.Message.Usage)
				usageSeen = true
			}

		case "content_block_start":
			if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
				toolsByIndex[event.Index] = &streamTool{
					id:   event.ContentBlock.ID,
					name: event.ContentBlock.Name,
				}
				toolOrder = append(toolOrder, event.Index)
			}

		case "content_block_delta":
			if event.Delta == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				text.WriteString(event.Delta.Text)
				if event.Delta.Text != "" && cb.OnTextDelta != nil {
					cb.OnTextDelta(event.Delta.Text)
				}
			case "thinking_delta":
				reasoning.WriteString(event.Delta.Thinking)
				if event.Delta.Thinking != "" && cb.OnReasoningDelta != nil {
					cb.OnReasoningDelta(event.Delta.Thinking)
				}
			case "signature_delta":
				signature.WriteString(event.Delta.Signature)
			case "input_json_delta":
				if t, ok := toolsByIndex[event.Index]; ok {
					t.args.WriteString(event.Delta.PartialJSON)
				}
			}

		case "message_delta":
			if event.Usage != nil {
				mergeAnthropicUsage(usage, event.Usage)
				usageSeen = true
			}

		case "message_stop":
			// The next Scan will see the end of the body; breaking here keeps a
			// server that keeps the connection open from holding the call.
			goto done
		}
	}

done:
	if err := scanner.Err(); err != nil {
		tlog.Warn("llm.provider", "sse_scan_error", "backend", "anthropic", "error", err.Error())
		return nil, fmt.Errorf("anthropic stream: %w", contextRequestError(ctx, err))
	}

	result.Content = text.String()
	result.ReasoningContent = reasoning.String()
	result.ReasoningSignature = signature.String()
	if usageSeen {
		result.Usage = usage.toUsage()
	}

	for _, idx := range toolOrder {
		t := toolsByIndex[idx]
		args := t.args.String()
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		result.ToolCalls = append(result.ToolCalls, types.ToolCall{
			ID: t.id, Name: t.name, Arguments: args,
		})
	}

	if len(result.ToolCalls) > 0 {
		tlog.Debug("llm.provider", "response", "backend", "anthropic", "model", p.model,
			"tool_calls", len(result.ToolCalls), "duration", time.Since(start).Round(time.Millisecond).String())
	} else {
		tlog.Debug("llm.provider", "response", "backend", "anthropic", "model", p.model,
			"content_len", len(result.Content), "duration", time.Since(start).Round(time.Millisecond).String())
	}
	return result, nil
}

// mergeAnthropicUsage overlays the counters an event carried, leaving every lane
// the event did not mention at its previous value. message_start and
// message_delta split the usage between them, so a later event is a correction
// rather than a replacement.
func mergeAnthropicUsage(dst, src *anthropicUsage) {
	if src == nil {
		return
	}
	if src.InputTokens != nil {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens != nil {
		dst.OutputTokens = src.OutputTokens
	}
	if src.CacheCreationInputTokens != nil {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
	}
	if src.CacheReadInputTokens != nil {
		dst.CacheReadInputTokens = src.CacheReadInputTokens
	}
}
