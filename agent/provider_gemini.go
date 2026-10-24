package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// Bounds on one native Gemini request, for the same reasons the other backends
// carry a pair: a batch answer is bounded as a whole, a stream only between
// events. They are variables so the tests can shrink them.
var (
	geminiRequestTimeout = 120 * time.Second
	geminiIdleTimeout    = 2 * time.Minute
)

// GeminiProvider implements LLMProvider against the native Generative Language
// API (generateContent / streamGenerateContent).
//
// The compatible endpoint accepts the OpenAI shape but reports Gemini's reasoning
// as ordinary text and loses `thoughtsTokenCount`, `cachedContentTokenCount` and
// the `thought` flag on a part. This backend reads the native fields and maps
// them onto the shared usage lanes (roadmap A6).
type GeminiProvider struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client

	// thinkingBudget is passed to thinkingConfig.thinkingBudget. Zero sends no
	// thinkingConfig, which leaves the model's own default in charge.
	thinkingBudget int

	route        string
	costCurrency string
	cache        PromptCachePolicy
}

// NewGeminiProvider creates a native Generative Language API provider. An empty
// base URL means the public endpoint.
func NewGeminiProvider(apiKey, baseURL, model string) *GeminiProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	return &GeminiProvider{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{},
	}
}

// SetThinkingBudget asks the route to include its thoughts. includeThoughts is
// sent with the budget: a budget without it would pay for reasoning the caller
// never sees.
func (p *GeminiProvider) SetThinkingBudget(tokens int) { p.thinkingBudget = tokens }

// SetRouteInfo names the configuration route, its default model and the unit its
// reported charge is denominated in.
func (p *GeminiProvider) SetRouteInfo(route, model, costCurrency string) {
	p.route = route
	if model != "" {
		p.model = model
	}
	p.costCurrency = costCurrency
}

// Route returns the configuration name the user gave this provider.
func (p *GeminiProvider) Route() string { return p.route }

// DefaultModel returns the model this provider uses when a request names none.
func (p *GeminiProvider) DefaultModel() string { return p.model }

// CostCurrency returns the unit this route bills a reported charge in.
func (p *GeminiProvider) CostCurrency() string { return p.costCurrency }

// SetPromptCache records the route's policy. Gemini caches a stable prefix
// automatically, so "auto" changes no request field; "explicit" is refused when
// the provider is built because the cachedContents resource is not implemented.
func (p *GeminiProvider) SetPromptCache(policy PromptCachePolicy) { p.cache = policy }

// Name names the backend and model for display.
func (p *GeminiProvider) Name() string { return fmt.Sprintf("gemini/%s", p.model) }

// modelPath strips the optional "models/" prefix a user may paste from the
// documentation: the URL already supplies it, and doubling it would 404.
func geminiModelPath(model string) string {
	return strings.TrimPrefix(model, "models/")
}

// endpoint builds the request URL for one model and shape.
func (p *GeminiProvider) endpoint(model string, stream bool) string {
	base := p.baseURL + "/models/" + url.PathEscape(geminiModelPath(model))
	if stream {
		return base + ":streamGenerateContent?alt=sse"
	}
	return base + ":generateContent"
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiFunctionDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type geminiToolGroup struct {
	FunctionDeclarations []geminiFunctionDecl `json:"functionDeclarations"`
}

type geminiThinkingConfig struct {
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
	ThinkingBudget  int  `json:"thinkingBudget,omitempty"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens int                   `json:"maxOutputTokens,omitempty"`
	ThinkingConfig  *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

type geminiRequest struct {
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	Contents          []geminiContent         `json:"contents"`
	Tools             []geminiToolGroup       `json:"tools,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiUsage struct {
	PromptTokenCount        *int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    *int `json:"candidatesTokenCount,omitempty"`
	TotalTokenCount         *int `json:"totalTokenCount,omitempty"`
	CachedContentTokenCount *int `json:"cachedContentTokenCount,omitempty"`
	ThoughtsTokenCount      *int `json:"thoughtsTokenCount,omitempty"`
}

// toUsage maps Gemini's counters onto the shared lanes.
//
// candidatesTokenCount excludes the thought tokens (the API's total is prompt +
// candidates + thoughts), while types.Usage defines reasoning as part of the
// completion count used for pricing. The completion lane is therefore the sum,
// and ReasoningTokens marks the part of it that was thinking.
func (u *geminiUsage) toUsage() *types.Usage {
	if u == nil {
		return nil
	}
	if u.PromptTokenCount == nil && u.CandidatesTokenCount == nil &&
		u.TotalTokenCount == nil && u.ThoughtsTokenCount == nil &&
		u.CachedContentTokenCount == nil {
		return nil
	}
	var prompt, candidates, thoughts, cached int
	if u.PromptTokenCount != nil {
		prompt = *u.PromptTokenCount
	}
	if u.CandidatesTokenCount != nil {
		candidates = *u.CandidatesTokenCount
	}
	if u.ThoughtsTokenCount != nil {
		thoughts = *u.ThoughtsTokenCount
	}
	if u.CachedContentTokenCount != nil {
		cached = *u.CachedContentTokenCount
	}
	completion := candidates + thoughts
	return &types.Usage{
		PromptTokens:       prompt,
		CompletionTokens:   completion,
		TotalTokens:        prompt + completion,
		CachedPromptTokens: cached,
		ReasoningTokens:    thoughts,
	}
}

// Chat sends one generateContent request and reads its answer.
func (p *GeminiProvider) Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	body, err := p.requestBody(req)
	if err != nil {
		return nil, err
	}
	tlog.Trace("llm.provider", "request", "backend", "gemini", "model", model, "body", string(body))

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
		tlog.Warn("llm.provider", "retrying", "backend", "gemini", "attempt", attempt+1,
			"of", maxAttempts, "in", delay.Round(time.Millisecond).String(), "error", err.Error())
		notifyRetry(req.StreamCallbacks, attempt+1, maxAttempts, err)
		if serr := sleepCtx(ctx, delay); serr != nil {
			return nil, fmt.Errorf("gemini retry wait: %w", serr)
		}
	}
	return nil, attemptsError(attempts, lastErr)
}

// geminiSchema translates a JSON Schema object into the subset Gemini's Schema
// message accepts: types in upper case, nested properties and items translated
// recursively, and fields Gemini does not define dropped rather than guessed.
func geminiSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		switch key {
		case "type":
			if s, ok := value.(string); ok {
				out[key] = strings.ToUpper(s)
			}
		case "properties":
			props, ok := value.(map[string]any)
			if !ok {
				continue
			}
			translated := make(map[string]any, len(props))
			for name, raw := range props {
				if child, ok := raw.(map[string]any); ok {
					translated[name] = geminiSchema(child)
				}
			}
			out[key] = translated
		case "items":
			if child, ok := value.(map[string]any); ok {
				out[key] = geminiSchema(child)
			}
		case "required", "enum", "description", "format", "nullable",
			"minItems", "maxItems", "minimum", "maximum", "pattern":
			out[key] = value
		}
	}
	return out
}

func (p *GeminiProvider) requestBody(req types.ChatRequest) ([]byte, error) {
	system, contents := geminiContents(req.Messages)

	out := geminiRequest{
		Contents: contents,
	}
	if system != "" {
		out.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: system}}}
	}

	if len(req.Tools) > 0 {
		decls := make([]geminiFunctionDecl, len(req.Tools))
		for i, td := range req.Tools {
			decls[i] = geminiFunctionDecl{
				Name:        td.Name,
				Description: td.Description,
				// Gemini's Schema type is an enum spelled in upper case, unlike
				// the JSON Schema the shared tool definitions use. The schema is
				// translated rather than passed through, or every tool call would
				// be rejected for an invalid enum value.
				Parameters: geminiSchema(td.Parameters),
			}
		}
		out.Tools = []geminiToolGroup{{FunctionDeclarations: decls}}
	}

	gen := &geminiGenerationConfig{}
	if req.MaxTokens > 0 {
		gen.MaxOutputTokens = req.MaxTokens
	}
	if p.thinkingBudget > 0 {
		gen.ThinkingConfig = &geminiThinkingConfig{
			IncludeThoughts: true,
			ThinkingBudget:  p.thinkingBudget,
		}
	}
	out.GenerationConfig = gen

	return json.Marshal(out)
}

// geminiContents maps the shared history onto contents/systemInstruction.
//
// Gemini calls the assistant role "model", answers a tool call with a
// functionResponse part on a user turn, and keeps consecutive tool results in
// one content. The system text is a separate instruction rather than a turn.
func geminiContents(msgs []types.Message) (system string, contents []geminiContent) {
	var systemParts []string
	for _, m := range msgs {
		if m.Role == types.RoleSystem && m.Content != "" {
			systemParts = append(systemParts, m.Content)
		}
	}
	system = strings.Join(systemParts, "\n\n")

	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case types.RoleSystem:
			continue

		case types.RoleTool:
			var parts []geminiPart
			for ; i < len(msgs) && msgs[i].Role == types.RoleTool; i++ {
				t := msgs[i]
				name := t.Name
				if name == "" {
					name = t.ToolCallID
				}
				parts = append(parts, geminiPart{FunctionResponse: &geminiFunctionResponse{
					Name:     name,
					Response: map[string]any{"result": t.Content},
				}})
			}
			i--
			contents = append(contents, geminiContent{Role: "user", Parts: parts})

		case types.RoleAssistant:
			var parts []geminiPart
			// Gemini's thought parts are not replayed: the native API treats them
			// as a report of the model's reasoning, and the turn does not need
			// them to continue. The visible text and the calls are enough.
			if m.Content != "" {
				parts = append(parts, geminiPart{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{
					Name: tc.Name,
					Args: toolInput(tc.Arguments),
				}})
			}
			if len(parts) > 0 {
				contents = append(contents, geminiContent{Role: "model", Parts: parts})
			}

		default: // user
			if m.Content == "" {
				continue
			}
			contents = append(contents, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: m.Content}},
			})
		}
	}
	return system, contents
}

func (p *GeminiProvider) attempt(ctx context.Context, model string, body []byte, cb *types.StreamCallbacks) (*types.ChatResponse, error) {
	reqCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	var watchdog *time.Timer
	if cb != nil {
		watchdog = time.AfterFunc(geminiIdleTimeout, func() {
			cancel(fmt.Errorf("%w for %s", errStreamSilent, geminiIdleTimeout))
		})
	} else {
		watchdog = time.AfterFunc(geminiRequestTimeout, func() {
			cancel(fmt.Errorf("no answer within %s: %w", geminiRequestTimeout, context.DeadlineExceeded))
		})
	}
	defer watchdog.Stop()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.endpoint(model, cb != nil), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gemini request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", p.apiKey)

	httpResp, err := p.client.Do(httpReq)
	start := time.Now()
	if err != nil {
		tlog.Error("llm.provider", "api error", "backend", "gemini", "error", err)
		return nil, fmt.Errorf("gemini api: %w", contextRequestError(reqCtx, err))
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		tlog.Error("llm.provider", "api error", "backend", "gemini", "status", httpResp.StatusCode)
		return nil, fmt.Errorf("gemini api: %w", &statusError{
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

type geminiWireResponse struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *geminiUsage `json:"usageMetadata"`
}

func (p *GeminiProvider) chatBatch(body io.ReadCloser, start time.Time) (*types.ChatResponse, error) {
	defer body.Close()

	var wire geminiWireResponse
	if err := json.NewDecoder(body).Decode(&wire); err != nil {
		return nil, fmt.Errorf("gemini decode: %w", err)
	}
	if len(wire.Candidates) == 0 {
		return nil, fmt.Errorf("no candidates returned")
	}

	result := &types.ChatResponse{}
	applyGeminiParts(result, wire.Candidates[0].Content.Parts, nil)
	result.Usage = wire.UsageMetadata.toUsage()

	tlog.Debug("llm.provider", "response", "backend", "gemini", "model", p.model,
		"content_len", len(result.Content), "tool_calls", len(result.ToolCalls),
		"duration", time.Since(start).Round(time.Millisecond).String())
	return result, nil
}

// applyGeminiParts flattens one candidate's parts into the shared response. When
// cb is non-nil the text parts are delivered as deltas; the batch path passes nil
// and only accumulates.
func applyGeminiParts(result *types.ChatResponse, parts []geminiPart, cb *types.StreamCallbacks) {
	var text, reasoning strings.Builder
	text.WriteString(result.Content)
	reasoning.WriteString(result.ReasoningContent)

	for _, part := range parts {
		switch {
		case part.FunctionCall != nil:
			args := string(part.FunctionCall.Args)
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			result.ToolCalls = append(result.ToolCalls, types.ToolCall{
				// Gemini has no call id of its own; the result is correlated by
				// name, so the name is the most useful identity to carry.
				ID:        part.FunctionCall.Name,
				Name:      part.FunctionCall.Name,
				Arguments: args,
			})
		case part.Text != "" && part.Thought:
			reasoning.WriteString(part.Text)
			if cb != nil && cb.OnReasoningDelta != nil {
				cb.OnReasoningDelta(part.Text)
			}
		case part.Text != "":
			text.WriteString(part.Text)
			if cb != nil && cb.OnTextDelta != nil {
				cb.OnTextDelta(part.Text)
			}
		}
	}
	result.Content = text.String()
	result.ReasoningContent = reasoning.String()
}

func (p *GeminiProvider) chatStream(ctx context.Context, body io.ReadCloser, start time.Time, cb *types.StreamCallbacks, watchdog *time.Timer) (*types.ChatResponse, error) {
	defer body.Close()

	result := &types.ChatResponse{}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	for scanner.Scan() {
		if watchdog != nil {
			watchdog.Reset(geminiIdleTimeout)
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}

		var chunk geminiWireResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			tlog.Debug("llm.provider", "sse_parse_error", "backend", "gemini", "error", err.Error())
			continue
		}
		// The final chunk repeats the whole usage record, so the last one wins.
		if chunk.UsageMetadata != nil {
			result.Usage = chunk.UsageMetadata.toUsage()
		}
		for _, candidate := range chunk.Candidates {
			applyGeminiParts(result, candidate.Content.Parts, cb)
		}
	}

	if err := scanner.Err(); err != nil {
		tlog.Warn("llm.provider", "sse_scan_error", "backend", "gemini", "error", err.Error())
		return nil, fmt.Errorf("gemini stream: %w", contextRequestError(ctx, err))
	}

	tlog.Debug("llm.provider", "response", "backend", "gemini", "model", p.model,
		"content_len", len(result.Content), "tool_calls", len(result.ToolCalls),
		"duration", time.Since(start).Round(time.Millisecond).String())
	return result, nil
}
