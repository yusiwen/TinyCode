package agent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// Bounds on one native Bedrock request. The stream bound is the silence between
// event frames, not the length of the generation: a long answer that keeps
// arriving is not cut off.
var (
	bedrockRequestTimeout = 120 * time.Second
	bedrockIdleTimeout    = 2 * time.Minute
)

// maxBedrockFrame caps one event-stream frame. A frame carries one small event;
// a length field in the gigabytes is a corrupt stream, not an answer.
const maxBedrockFrame = 16 << 20

// BedrockProvider implements LLMProvider against the Bedrock Converse API.
//
// Converse is the vendor-neutral shape Bedrock exposes for every hosted model, so
// one mapping serves Anthropic, Llama and Mistral models. Going through an
// OpenAI-compatible gateway instead loses the native reasoningContent blocks and
// the cacheReadInputTokens/cacheWriteInputTokens lanes (roadmap A6).
//
// Requests are signed with SigV4 from environment credentials. Credential
// providers other than the environment — an instance profile, SSO, a credential
// file — are deliberately not implemented: silently reaching IMDS from an agent
// process is a different security decision than reading two variables.
type BedrockProvider struct {
	accessKey    string
	secretKey    string
	sessionToken string
	region       string
	baseURL      string
	model        string
	client       *http.Client

	thinkingBudget int
	route          string
	costCurrency   string
	cache          PromptCachePolicy

	// now is the signing clock. It is a field so a test can sign a request at a
	// fixed instant instead of asserting on time-varying values.
	now func() time.Time
}

// NewBedrockProvider creates a Converse provider. An empty base URL derives the
// regional endpoint from the region, which is also how a test points the
// provider at a local server without changing the signing fields.
func NewBedrockProvider(accessKey, secretKey, sessionToken, region, baseURL, model string) *BedrockProvider {
	if strings.TrimSpace(baseURL) == "" && region != "" {
		baseURL = "https://bedrock-runtime." + region + ".amazonaws.com"
	}
	return &BedrockProvider{
		accessKey:    accessKey,
		secretKey:    secretKey,
		sessionToken: sessionToken,
		region:       region,
		baseURL:      strings.TrimRight(baseURL, "/"),
		model:        model,
		client:       &http.Client{},
		now:          time.Now,
	}
}

// SetThinkingBudget asks the hosted model to reason first. Converse carries it in
// additionalModelRequestFields because the budget is a model-specific field, not
// part of the neutral schema.
func (p *BedrockProvider) SetThinkingBudget(tokens int) { p.thinkingBudget = tokens }

// SetRouteInfo names the configuration route, its default model and the unit its
// reported charge is denominated in.
func (p *BedrockProvider) SetRouteInfo(route, model, costCurrency string) {
	p.route = route
	if model != "" {
		p.model = model
	}
	p.costCurrency = costCurrency
}

// Route returns the configuration name the user gave this provider.
func (p *BedrockProvider) Route() string { return p.route }

// DefaultModel returns the model this provider uses when a request names none.
func (p *BedrockProvider) DefaultModel() string { return p.model }

// CostCurrency returns the unit this route bills a reported charge in.
func (p *BedrockProvider) CostCurrency() string { return p.costCurrency }

// SetPromptCache records the route's policy. Converse caches what follows a
// cachePoint block, so only "explicit" has an effect; the builder refuses the
// other modes rather than silently sending no hint.
func (p *BedrockProvider) SetPromptCache(policy PromptCachePolicy) { p.cache = policy }

// Name names the backend and model for display.
func (p *BedrockProvider) Name() string { return fmt.Sprintf("bedrock/%s", p.model) }

func (p *BedrockProvider) endpoint(model string, stream bool) string {
	base := p.baseURL + "/model/" + url.PathEscape(model)
	if stream {
		return base + "/converse-stream"
	}
	return base + "/converse"
}

// bedrockContentBlock is one block in a Converse message.
type bedrockContentBlock struct {
	// Text is a pointer so an empty tool result still sends the required text
	// field: a command that printed nothing is a result, not an absent block.
	Text             *string                  `json:"text,omitempty"`
	ToolUse          *bedrockToolUse          `json:"toolUse,omitempty"`
	ToolResult       *bedrockToolResult       `json:"toolResult,omitempty"`
	ReasoningContent *bedrockReasoningContent `json:"reasoningContent,omitempty"`
	CachePoint       *bedrockCachePoint       `json:"cachePoint,omitempty"`
}

type bedrockToolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

type bedrockToolResult struct {
	ToolUseID string                `json:"toolUseId"`
	Content   []bedrockContentBlock `json:"content"`
	Status    string                `json:"status,omitempty"`
}

type bedrockReasoningContent struct {
	ReasoningText *struct {
		Text      string `json:"text,omitempty"`
		Signature string `json:"signature,omitempty"`
	} `json:"reasoningText,omitempty"`
}

type bedrockCachePoint struct {
	Type string `json:"type"`
}

type bedrockMessage struct {
	Role    string                `json:"role"`
	Content []bedrockContentBlock `json:"content"`
}

type bedrockSystemBlock struct {
	Text       *string            `json:"text,omitempty"`
	CachePoint *bedrockCachePoint `json:"cachePoint,omitempty"`
}

type bedrockInferenceConfig struct {
	MaxTokens int `json:"maxTokens,omitempty"`
}

type bedrockToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema struct {
		JSON map[string]any `json:"json"`
	} `json:"inputSchema"`
}

type bedrockTool struct {
	ToolSpec   *bedrockToolSpec   `json:"toolSpec,omitempty"`
	CachePoint *bedrockCachePoint `json:"cachePoint,omitempty"`
}

type bedrockToolConfig struct {
	Tools []bedrockTool `json:"tools"`
}

type bedrockRequest struct {
	Messages                     []bedrockMessage        `json:"messages"`
	System                       []bedrockSystemBlock    `json:"system,omitempty"`
	InferenceConfig              *bedrockInferenceConfig `json:"inferenceConfig,omitempty"`
	ToolConfig                   *bedrockToolConfig      `json:"toolConfig,omitempty"`
	AdditionalModelRequestFields map[string]any          `json:"additionalModelRequestFields,omitempty"`
}

type bedrockUsage struct {
	InputTokens           *int `json:"inputTokens,omitempty"`
	OutputTokens          *int `json:"outputTokens,omitempty"`
	TotalTokens           *int `json:"totalTokens,omitempty"`
	CacheReadInputTokens  *int `json:"cacheReadInputTokens,omitempty"`
	CacheWriteInputTokens *int `json:"cacheWriteInputTokens,omitempty"`
}

// toUsage maps Converse's counters onto the shared lanes.
//
// Unlike the Anthropic Messages API, Converse's inputTokens is the whole prompt
// and the two cache counters are subsets of it, which is the same relationship
// types.Usage documents. They are therefore copied, not added.
func (u *bedrockUsage) toUsage() *types.Usage {
	if u == nil {
		return nil
	}
	if u.InputTokens == nil && u.OutputTokens == nil && u.TotalTokens == nil &&
		u.CacheReadInputTokens == nil && u.CacheWriteInputTokens == nil {
		return nil
	}
	var input, output, total, cached, written int
	if u.InputTokens != nil {
		input = *u.InputTokens
	}
	if u.OutputTokens != nil {
		output = *u.OutputTokens
	}
	if u.TotalTokens != nil {
		total = *u.TotalTokens
	}
	if u.CacheReadInputTokens != nil {
		cached = *u.CacheReadInputTokens
	}
	if u.CacheWriteInputTokens != nil {
		written = *u.CacheWriteInputTokens
	}
	if total == 0 {
		total = input + output
	}
	return &types.Usage{
		PromptTokens:       input,
		CompletionTokens:   output,
		TotalTokens:        total,
		CachedPromptTokens: cached,
		CacheWriteTokens:   written,
	}
}

// Chat sends one Converse request and reads its answer.
func (p *BedrockProvider) Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	if p.accessKey == "" || p.secretKey == "" {
		return nil, fmt.Errorf("bedrock config: no AWS credentials (set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY)")
	}
	if p.region == "" {
		return nil, fmt.Errorf("bedrock config: no region (set region, AWS_REGION or AWS_DEFAULT_REGION)")
	}
	if p.baseURL == "" {
		return nil, fmt.Errorf("bedrock config: no endpoint (set base_url or a region)")
	}

	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	body, err := p.requestBody(req)
	if err != nil {
		return nil, err
	}
	tlog.Trace("llm.provider", "request", "backend", "bedrock", "model", model, "body", string(body))

	maxAttempts := retryMaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	attempts := 0
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attempts = attempt
		tracker := trackedCallbacks(req.StreamCallbacks)
		resp, err := p.attempt(ctx, model, body, tracker.callbacks())
		if err == nil {
			resp.Route, resp.Model = p.route, model
			return resp, nil
		}
		lastErr = err
		if !shouldRetry(ctx, err, tracker.deliveredSomething(), attempt) {
			break
		}
		delay := backoffDelay(attempt, retryAfterOf(err))
		tlog.Warn("llm.provider", "retrying", "backend", "bedrock", "attempt", attempt+1,
			"of", maxAttempts, "in", delay.Round(time.Millisecond).String(), "error", err.Error())
		notifyRetry(req.StreamCallbacks, attempt+1, maxAttempts, err)
		if serr := sleepCtx(ctx, delay); serr != nil {
			return nil, fmt.Errorf("bedrock retry wait: %w", serr)
		}
	}
	return nil, attemptsError(attempts, lastErr)
}

func (p *BedrockProvider) requestBody(req types.ChatRequest) ([]byte, error) {
	system, messages := bedrockMessages(req.Messages)

	out := bedrockRequest{
		Messages: messages,
		System:   system,
	}
	if req.MaxTokens > 0 {
		out.InferenceConfig = &bedrockInferenceConfig{MaxTokens: req.MaxTokens}
	}

	if len(req.Tools) > 0 {
		tools := make([]bedrockTool, len(req.Tools))
		for i, td := range req.Tools {
			spec := &bedrockToolSpec{Name: td.Name, Description: td.Description}
			spec.InputSchema.JSON = td.Parameters
			tools[i] = bedrockTool{ToolSpec: spec}
		}
		out.ToolConfig = &bedrockToolConfig{Tools: tools}
	}

	if p.thinkingBudget > 0 {
		out.AdditionalModelRequestFields = map[string]any{
			"thinking": map[string]any{"type": "enabled", "budget_tokens": p.thinkingBudget},
		}
	}

	// A cachePoint marks the end of the stable prefix. Converse accepts it in the
	// system array, in the tool list, or in a message; the system array is the
	// shape that covers tools + system, so it is used when there is a system
	// block, and the tool list is the fallback when there is not.
	if p.cache.PlacesBreakpoints() {
		if len(out.System) > 0 {
			out.System = append(out.System, bedrockSystemBlock{CachePoint: &bedrockCachePoint{Type: "default"}})
		} else if out.ToolConfig != nil && len(out.ToolConfig.Tools) > 0 {
			out.ToolConfig.Tools = append(out.ToolConfig.Tools, bedrockTool{CachePoint: &bedrockCachePoint{Type: "default"}})
		}
	}

	return json.Marshal(out)
}

// bedrockText wraps a text value for a content block. The pointer distinguishes
// "this block is text" from "this block is a tool call"; an empty string is still
// a text block.
func bedrockText(text string) *string { return &text }

// bedrockMessages maps the shared history onto Converse's messages/system split.
// Consecutive tool results are grouped into one user turn because a toolUse must
// be answered in the next user message.
func bedrockMessages(msgs []types.Message) (system []bedrockSystemBlock, messages []bedrockMessage) {
	for _, m := range msgs {
		if m.Role == types.RoleSystem && m.Content != "" {
			system = append(system, bedrockSystemBlock{Text: bedrockText(m.Content)})
		}
	}

	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case types.RoleSystem:
			continue

		case types.RoleTool:
			var results []bedrockContentBlock
			for ; i < len(msgs) && msgs[i].Role == types.RoleTool; i++ {
				t := msgs[i]
				id := t.ToolCallID
				if id == "" {
					id = t.Name
				}
				results = append(results, bedrockContentBlock{ToolResult: &bedrockToolResult{
					ToolUseID: id,
					Content:   []bedrockContentBlock{{Text: bedrockText(t.Content)}},
				}})
			}
			i--
			messages = append(messages, bedrockMessage{Role: "user", Content: results})

		case types.RoleAssistant:
			var blocks []bedrockContentBlock
			if m.ReasoningContent != "" && m.ReasoningSignature != "" {
				blocks = append(blocks, bedrockContentBlock{ReasoningContent: &bedrockReasoningContent{
					ReasoningText: &struct {
						Text      string `json:"text,omitempty"`
						Signature string `json:"signature,omitempty"`
					}{Text: m.ReasoningContent, Signature: m.ReasoningSignature},
				}})
			}
			if m.Content != "" {
				blocks = append(blocks, bedrockContentBlock{Text: bedrockText(m.Content)})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, bedrockContentBlock{ToolUse: &bedrockToolUse{
					ToolUseID: tc.ID,
					Name:      tc.Name,
					Input:     toolInput(tc.Arguments),
				}})
			}
			if len(blocks) > 0 {
				messages = append(messages, bedrockMessage{Role: "assistant", Content: blocks})
			}

		default: // user
			if m.Content == "" {
				continue
			}
			messages = append(messages, bedrockMessage{
				Role:    "user",
				Content: []bedrockContentBlock{{Text: bedrockText(m.Content)}},
			})
		}
	}
	return system, messages
}

func (p *BedrockProvider) attempt(ctx context.Context, model string, body []byte, cb *types.StreamCallbacks) (*types.ChatResponse, error) {
	reqCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var watchdog *time.Timer
	if cb != nil {
		watchdog = time.AfterFunc(bedrockIdleTimeout, func() {
			cancel(fmt.Errorf("%w for %s", errStreamSilent, bedrockIdleTimeout))
		})
	} else {
		watchdog = time.AfterFunc(bedrockRequestTimeout, func() {
			cancel(fmt.Errorf("no answer within %s: %w", bedrockRequestTimeout, context.DeadlineExceeded))
		})
	}
	defer watchdog.Stop()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.endpoint(model, cb != nil), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("bedrock request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if cb != nil {
		httpReq.Header.Set("Accept", "application/vnd.amazon.eventstream")
	}
	if err := p.sign(httpReq, body, p.now()); err != nil {
		return nil, err
	}

	httpResp, err := p.client.Do(httpReq)
	start := time.Now()
	if err != nil {
		tlog.Error("llm.provider", "api error", "backend", "bedrock", "error", err)
		return nil, fmt.Errorf("bedrock api: %w", contextRequestError(reqCtx, err))
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		tlog.Error("llm.provider", "api error", "backend", "bedrock", "status", httpResp.StatusCode)
		return nil, fmt.Errorf("bedrock api: %w", &statusError{
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

type bedrockWireResponse struct {
	Output struct {
		Message struct {
			Role    string                `json:"role"`
			Content []bedrockContentBlock `json:"content"`
		} `json:"message"`
	} `json:"output"`
	StopReason string        `json:"stopReason"`
	Usage      *bedrockUsage `json:"usage"`
}

func (p *BedrockProvider) chatBatch(body io.ReadCloser, start time.Time) (*types.ChatResponse, error) {
	defer body.Close()

	var wire bedrockWireResponse
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, fmt.Errorf("bedrock decode: %w", err)
	}
	if len(wire.Output.Message.Content) == 0 {
		return nil, fmt.Errorf("no content blocks returned")
	}

	result := &types.ChatResponse{}
	applyBedrockBlocks(result, wire.Output.Message.Content)
	result.Usage = wire.Usage.toUsage()

	tlog.Debug("llm.provider", "response", "backend", "bedrock", "model", p.model,
		"content_len", len(result.Content), "tool_calls", len(result.ToolCalls),
		"duration", time.Since(start).Round(time.Millisecond).String())
	return result, nil
}

func applyBedrockBlocks(result *types.ChatResponse, blocks []bedrockContentBlock) {
	var text, reasoning strings.Builder
	for _, b := range blocks {
		switch {
		case b.ToolUse != nil:
			args := string(b.ToolUse.Input)
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			result.ToolCalls = append(result.ToolCalls, types.ToolCall{
				ID:        b.ToolUse.ToolUseID,
				Name:      b.ToolUse.Name,
				Arguments: args,
			})
		case b.ReasoningContent != nil && b.ReasoningContent.ReasoningText != nil:
			reasoning.WriteString(b.ReasoningContent.ReasoningText.Text)
			if b.ReasoningContent.ReasoningText.Signature != "" {
				result.ReasoningSignature = b.ReasoningContent.ReasoningText.Signature
			}
		case b.Text != nil:
			text.WriteString(*b.Text)
		}
	}
	result.Content = text.String()
	result.ReasoningContent = reasoning.String()
}

// bedrockStreamEvent is one decoded ConverseStream payload.
type bedrockStreamEvent struct {
	ContentBlockIndex *int `json:"contentBlockIndex,omitempty"`
	Start             *struct {
		ToolUse *bedrockToolUse `json:"toolUse,omitempty"`
	} `json:"start,omitempty"`
	Delta *struct {
		Text    string `json:"text,omitempty"`
		ToolUse *struct {
			Input string `json:"input,omitempty"`
		} `json:"toolUse,omitempty"`
		ReasoningContent *struct {
			Text      string `json:"text,omitempty"`
			Signature string `json:"signature,omitempty"`
		} `json:"reasoningContent,omitempty"`
	} `json:"delta,omitempty"`
	StopReason string        `json:"stopReason,omitempty"`
	Usage      *bedrockUsage `json:"usage,omitempty"`
}

func (p *BedrockProvider) chatStream(ctx context.Context, body io.ReadCloser, start time.Time, cb *types.StreamCallbacks, watchdog *time.Timer) (*types.ChatResponse, error) {
	defer body.Close()

	result := &types.ChatResponse{}
	var text, reasoning, signature strings.Builder

	type streamTool struct {
		id   string
		name string
		args strings.Builder
	}
	toolsByIndex := map[int]*streamTool{}
	var toolOrder []int

	for {
		eventType, payload, err := readBedrockFrame(body)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bedrock stream: %w", contextRequestError(ctx, err))
		}
		if watchdog != nil {
			watchdog.Reset(bedrockIdleTimeout)
		}

		if eventType != "contentBlockDelta" && eventType != "messageStart" &&
			eventType != "contentBlockStart" && eventType != "contentBlockStop" &&
			eventType != "messageStop" && eventType != "metadata" {
			// An exception frame is named by its headers, which readBedrockFrame
			// reports as the type; anything else is a frame this parser does not
			// know and can safely skip.
			if strings.HasPrefix(eventType, "exception:") || strings.HasPrefix(eventType, "error:") {
				return nil, fmt.Errorf("bedrock stream: %s: %s", eventType, string(payload))
			}
			continue
		}

		var event bedrockStreamEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, fmt.Errorf("bedrock decode: %w", err)
		}

		switch eventType {
		case "contentBlockStart":
			if event.Start != nil && event.Start.ToolUse != nil && event.ContentBlockIndex != nil {
				toolsByIndex[*event.ContentBlockIndex] = &streamTool{
					id:   event.Start.ToolUse.ToolUseID,
					name: event.Start.ToolUse.Name,
				}
				toolOrder = append(toolOrder, *event.ContentBlockIndex)
			}

		case "contentBlockDelta":
			if event.Delta == nil {
				continue
			}
			if event.Delta.Text != "" {
				text.WriteString(event.Delta.Text)
				if cb.OnTextDelta != nil {
					cb.OnTextDelta(event.Delta.Text)
				}
			}
			if event.Delta.ReasoningContent != nil {
				if event.Delta.ReasoningContent.Text != "" {
					reasoning.WriteString(event.Delta.ReasoningContent.Text)
					if cb.OnReasoningDelta != nil {
						cb.OnReasoningDelta(event.Delta.ReasoningContent.Text)
					}
				}
				signature.WriteString(event.Delta.ReasoningContent.Signature)
			}
			if event.Delta.ToolUse != nil && event.ContentBlockIndex != nil {
				if t, ok := toolsByIndex[*event.ContentBlockIndex]; ok {
					t.args.WriteString(event.Delta.ToolUse.Input)
				}
			}

		case "metadata":
			if event.Usage != nil {
				result.Usage = event.Usage.toUsage()
			}

		case "messageStop":
			// The metadata frame follows messageStop; the loop continues until
			// the stream ends so the final usage is not lost.
		}
	}

	result.Content = text.String()
	result.ReasoningContent = reasoning.String()
	result.ReasoningSignature = signature.String()

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

	tlog.Debug("llm.provider", "response", "backend", "bedrock", "model", p.model,
		"content_len", len(result.Content), "tool_calls", len(result.ToolCalls),
		"duration", time.Since(start).Round(time.Millisecond).String())
	return result, nil
}

// readBedrockFrame decodes one vnd.amazon.eventstream frame.
//
// The wire format is a prelude (total length, headers length, prelude CRC),
// a header block, a payload, and a message CRC. Both CRCs are validated: a
// truncated or interleaved frame would otherwise decode into a plausible event
// and corrupt the answer.
func readBedrockFrame(r io.Reader) (eventType string, payload []byte, err error) {
	prelude := make([]byte, 12)
	if _, err := io.ReadFull(r, prelude); err != nil {
		return "", nil, err
	}
	totalLen := binary.BigEndian.Uint32(prelude[0:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])
	wantPreludeCRC := binary.BigEndian.Uint32(prelude[8:12])
	if got := crc32.ChecksumIEEE(prelude[0:8]); got != wantPreludeCRC {
		return "", nil, fmt.Errorf("corrupt prelude CRC")
	}
	if totalLen < 16 || totalLen > maxBedrockFrame || headersLen > totalLen-16 {
		return "", nil, fmt.Errorf("invalid frame length %d", totalLen)
	}

	frame := make([]byte, totalLen)
	copy(frame, prelude)
	if _, err := io.ReadFull(r, frame[12:]); err != nil {
		return "", nil, err
	}
	wantMessageCRC := binary.BigEndian.Uint32(frame[totalLen-4:])
	if got := crc32.ChecksumIEEE(frame[:totalLen-4]); got != wantMessageCRC {
		return "", nil, fmt.Errorf("corrupt message CRC")
	}

	headers := frame[12 : 12+headersLen]
	payload = frame[12+headersLen : totalLen-4]

	values, err := parseBedrockHeaders(headers)
	if err != nil {
		return "", nil, err
	}
	if msgType := values[":message-type"]; msgType == "exception" || msgType == "error" {
		name := values[":exception-type"]
		if name == "" {
			name = values[":error-code"]
		}
		if name == "" {
			name = msgType
		}
		return msgType + ":" + name, payload, nil
	}
	return values[":event-type"], payload, nil
}

// parseBedrockHeaders reads the header block into string values. Only string and
// byte-array headers carry names this parser needs; every other type is skipped
// by its fixed width, so an unknown header cannot desynchronise the walk.
func parseBedrockHeaders(buf []byte) (map[string]string, error) {
	out := map[string]string{}
	for len(buf) > 0 {
		nameLen := int(buf[0])
		buf = buf[1:]
		if len(buf) < nameLen+1 {
			return nil, fmt.Errorf("truncated header name")
		}
		name := string(buf[:nameLen])
		valueType := buf[nameLen]
		buf = buf[nameLen+1:]

		switch valueType {
		case 0, 1: // bool true / false: no value bytes
			if valueType == 0 {
				out[name] = "true"
			} else {
				out[name] = "false"
			}
		case 2: // byte
			if len(buf) < 1 {
				return nil, fmt.Errorf("truncated byte header")
			}
			buf = buf[1:]
		case 3: // short
			if len(buf) < 2 {
				return nil, fmt.Errorf("truncated short header")
			}
			buf = buf[2:]
		case 4: // integer
			if len(buf) < 4 {
				return nil, fmt.Errorf("truncated integer header")
			}
			buf = buf[4:]
		case 5: // long
			if len(buf) < 8 {
				return nil, fmt.Errorf("truncated long header")
			}
			buf = buf[8:]
		case 6, 7: // byte array, string
			if len(buf) < 2 {
				return nil, fmt.Errorf("truncated header length")
			}
			n := int(binary.BigEndian.Uint16(buf[:2]))
			buf = buf[2:]
			if len(buf) < n {
				return nil, fmt.Errorf("truncated header value")
			}
			if valueType == 7 {
				out[name] = string(buf[:n])
			}
			buf = buf[n:]
		case 8: // timestamp
			if len(buf) < 8 {
				return nil, fmt.Errorf("truncated timestamp header")
			}
			buf = buf[8:]
		case 9: // uuid
			if len(buf) < 16 {
				return nil, fmt.Errorf("truncated uuid header")
			}
			buf = buf[16:]
		default:
			return nil, fmt.Errorf("unknown header type %d", valueType)
		}
	}
	return out, nil
}

// sign adds a SigV4 Authorization header for the bedrock service.
//
// The canonical request follows the published algorithm: the signed headers are
// host, x-amz-date and — when a session token is present — x-amz-security-token,
// sorted and lowercased; the payload is hashed with SHA-256.
func (p *BedrockProvider) sign(req *http.Request, body []byte, now time.Time) error {
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	headers := map[string]string{
		"host":       req.URL.Host,
		"x-amz-date": amzDate,
	}
	if p.sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", p.sessionToken)
		headers["x-amz-security-token"] = p.sessionToken
	}

	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.TrimSpace(headers[name]))
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(names, ";")

	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		canonicalHeaders.String(),
		signedHeaders,
		sha256Hex(body),
	}, "\n")

	scope := date + "/" + p.region + "/bedrock/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	key := hmacSHA256([]byte("AWS4"+p.secretKey), []byte(date))
	key = hmacSHA256(key, []byte(p.region))
	key = hmacSHA256(key, []byte("bedrock"))
	key = hmacSHA256(key, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		p.accessKey, scope, signedHeaders, signature))
	return nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}
