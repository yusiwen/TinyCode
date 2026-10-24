package agent

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// bedrockFrame encodes one vnd.amazon.eventstream frame, CRCs included, so the
// parser is exercised against the real framing rather than a shortcut.
func bedrockFrame(t *testing.T, headers map[string]string, payload string) []byte {
	t.Helper()

	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	var headerBuf bytes.Buffer
	for _, name := range names {
		value := headers[name]
		if len(name) > 255 || len(value) > 65535 {
			t.Fatalf("header %q is too long for the test encoder", name)
		}
		headerBuf.WriteByte(byte(len(name)))
		headerBuf.WriteString(name)
		headerBuf.WriteByte(7) // string
		_ = binary.Write(&headerBuf, binary.BigEndian, uint16(len(value)))
		headerBuf.WriteString(value)
	}

	total := 12 + headerBuf.Len() + len(payload) + 4
	frame := make([]byte, total)
	binary.BigEndian.PutUint32(frame[0:4], uint32(total))
	binary.BigEndian.PutUint32(frame[4:8], uint32(headerBuf.Len()))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[0:8]))
	copy(frame[12:], headerBuf.Bytes())
	copy(frame[12+headerBuf.Len():], payload)
	binary.BigEndian.PutUint32(frame[total-4:], crc32.ChecksumIEEE(frame[:total-4]))
	return frame
}

func bedrockEvent(t *testing.T, eventType, payload string) []byte {
	t.Helper()
	return bedrockFrame(t, map[string]string{
		":event-type":   eventType,
		":message-type": "event",
	}, payload)
}

// TestBedrockChatBatch covers Converse: the model path, SigV4 headers, the
// system/messages/toolConfig mapping, a cachePoint, thinking fields, reasoning
// replay and the cache usage lanes.
func TestBedrockChatBatch(t *testing.T) {
	var gotPath, gotAuth, gotDate string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotDate = r.Header.Get("X-Amz-Date")
		gotBody = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		io.WriteString(w, `{"output":{"message":{"role":"assistant","content":[`+
			`{"reasoningContent":{"reasoningText":{"text":"reasoned","signature":"sig-1"}}},`+
			`{"text":"hello"},`+
			`{"toolUse":{"toolUseId":"tool_1","name":"bash","input":{"cmd":"ls"}}}]}},`+
			`"stopReason":"tool_use",`+
			`"usage":{"inputTokens":100,"outputTokens":20,"totalTokens":120,`+
			`"cacheReadInputTokens":30,"cacheWriteInputTokens":5}}`)
	}))
	defer srv.Close()

	p := NewBedrockProvider("AKID", "SECRET", "", "us-east-1", srv.URL, "my-model")
	p.now = func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }
	p.SetRouteInfo("bedrock", "", "USD")
	p.SetThinkingBudget(2048)
	p.SetPromptCache(PromptCachePolicy{Mode: PromptCacheExplicit})

	resp, err := p.Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{
			{Role: types.RoleSystem, Content: "sys"},
			{Role: types.RoleUser, Content: "hi"},
			{Role: types.RoleAssistant, Content: "answer", ReasoningContent: "previous", ReasoningSignature: "sig-prev",
				ToolCalls: []types.ToolCall{{ID: "tool_9", Name: "read_file", Arguments: `{"path":"a"}`}}},
			{Role: types.RoleTool, Name: "read_file", ToolCallID: "tool_9", Content: "file body"},
		},
		Tools:     []types.ToolDef{{Name: "bash", Description: "run", Parameters: map[string]any{"type": "object"}}},
		MaxTokens: 128,
		Model:     "override-model",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if gotPath != "/model/override-model/converse" {
		t.Fatalf("request path = %q", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AKID/20261024/us-east-1/bedrock/aws4_request, "+
		"SignedHeaders=host;x-amz-date, Signature=") {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotDate != "20261024T120000Z" {
		t.Fatalf("X-Amz-Date = %q", gotDate)
	}

	system, _ := gotBody["system"].([]any)
	if len(system) != 2 {
		t.Fatalf("system = %v", gotBody["system"])
	}
	if block := system[0].(map[string]any); block["text"] != "sys" {
		t.Fatalf("system[0] = %v", block)
	}
	if cp, ok := system[1].(map[string]any)["cachePoint"].(map[string]any); !ok || cp["type"] != "default" {
		t.Fatalf("cachePoint = %v", system[1])
	}

	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3", len(msgs))
	}
	assistant, _ := msgs[1].(map[string]any)
	blocks, _ := assistant["content"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("assistant blocks = %v", blocks)
	}
	reasoning, _ := blocks[0].(map[string]any)["reasoningContent"].(map[string]any)
	text, _ := reasoning["reasoningText"].(map[string]any)
	if text["text"] != "previous" || text["signature"] != "sig-prev" {
		t.Fatalf("reasoning replay = %v", reasoning)
	}
	toolResult, _ := msgs[2].(map[string]any)["content"].([]any)
	result, _ := toolResult[0].(map[string]any)["toolResult"].(map[string]any)
	if result["toolUseId"] != "tool_9" {
		t.Fatalf("toolResult = %v", result)
	}

	fields, _ := gotBody["additionalModelRequestFields"].(map[string]any)
	thinking, _ := fields["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(2048) {
		t.Fatalf("additionalModelRequestFields = %v", fields)
	}

	if resp.Content != "hello" || resp.ReasoningContent != "reasoned" || resp.ReasoningSignature != "sig-1" {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "tool_1" || resp.ToolCalls[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.Route != "bedrock" || resp.Model != "override-model" {
		t.Errorf("served route/model = %q/%q", resp.Route, resp.Model)
	}
	u := resp.Usage
	if u == nil || u.PromptTokens != 100 || u.CompletionTokens != 20 || u.TotalTokens != 120 ||
		u.CachedPromptTokens != 30 || u.CacheWriteTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}
	if p.Name() != "bedrock/my-model" {
		t.Errorf("Name = %q", p.Name())
	}
}

// TestBedrockChatStream covers ConverseStream: binary event frames, text and
// reasoning deltas, a toolUse assembled from input deltas, and metadata usage.
func TestBedrockChatStream(t *testing.T) {
	var gotPath string
	var frames bytes.Buffer
	frames.Write(bedrockEvent(t, "messageStart", `{"role":"assistant"}`))
	frames.Write(bedrockEvent(t, "contentBlockStart",
		`{"contentBlockIndex":0,"start":{"toolUse":{"toolUseId":"tool_1","name":"bash"}}}`))
	frames.Write(bedrockEvent(t, "contentBlockDelta",
		`{"contentBlockIndex":1,"delta":{"reasoningContent":{"text":"plan","signature":"sig-1"}}}`))
	frames.Write(bedrockEvent(t, "contentBlockDelta", `{"contentBlockIndex":2,"delta":{"text":"Hel"}}`))
	frames.Write(bedrockEvent(t, "contentBlockDelta", `{"contentBlockIndex":2,"delta":{"text":"lo"}}`))
	frames.Write(bedrockEvent(t, "contentBlockDelta",
		`{"contentBlockIndex":0,"delta":{"toolUse":{"input":"{\"cmd\":"}}}`))
	frames.Write(bedrockEvent(t, "contentBlockDelta",
		`{"contentBlockIndex":0,"delta":{"toolUse":{"input":"\"ls\"}"}}}`))
	frames.Write(bedrockEvent(t, "contentBlockStop", `{"contentBlockIndex":0}`))
	frames.Write(bedrockEvent(t, "messageStop", `{"stopReason":"tool_use"}`))
	frames.Write(bedrockEvent(t, "metadata",
		`{"usage":{"inputTokens":100,"outputTokens":20,"totalTokens":120,"cacheReadInputTokens":30,"cacheWriteInputTokens":5}}`))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("stream request was not signed")
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(frames.Bytes())
	}))
	defer srv.Close()

	var reasoning, text []string
	p := NewBedrockProvider("AKID", "SECRET", "", "us-east-1", srv.URL, "my-model")
	resp, err := p.Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{{Role: types.RoleUser, Content: "hi"}},
		StreamCallbacks: &types.StreamCallbacks{
			OnReasoningDelta: func(s string) { reasoning = append(reasoning, s) },
			OnTextDelta:      func(s string) { text = append(text, s) },
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if gotPath != "/model/my-model/converse-stream" {
		t.Errorf("stream path = %q", gotPath)
	}
	if got := strings.Join(reasoning, ""); got != "plan" {
		t.Errorf("reasoning deltas = %q", got)
	}
	if got := strings.Join(text, ""); got != "Hello" {
		t.Errorf("text deltas = %q", got)
	}
	if resp.Content != "Hello" || resp.ReasoningContent != "plan" || resp.ReasoningSignature != "sig-1" {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "tool_1" || resp.ToolCalls[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	u := resp.Usage
	if u == nil || u.PromptTokens != 100 || u.CompletionTokens != 20 || u.CachedPromptTokens != 30 || u.CacheWriteTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}
}

// TestBedrockEmptyToolResultKeepsTheTextBlock: Converse requires content on a
// toolResult, and an empty command output is still a result.
func TestBedrockEmptyToolResultKeepsTheTextBlock(t *testing.T) {
	p := NewBedrockProvider("AKID", "SECRET", "", "us-east-1", "https://bedrock.invalid", "m")
	body, err := p.requestBody(types.ChatRequest{
		Messages: []types.Message{
			{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{ID: "t1", Name: "bash", Arguments: `{}`}}},
			{Role: types.RoleTool, Name: "bash", ToolCallID: "t1", Content: ""},
		},
	})
	if err != nil {
		t.Fatalf("requestBody: %v", err)
	}
	if !strings.Contains(string(body), `"text":""`) {
		t.Fatalf("empty tool result lost its text block: %s", body)
	}
}

// TestBedrockCachePointFallsBackToTools: without a system block the breakpoint
// goes on the tool list, which is the other place Converse accepts one.
func TestBedrockCachePointFallsBackToTools(t *testing.T) {
	p := NewBedrockProvider("AKID", "SECRET", "", "us-east-1", "https://bedrock.invalid", "m")
	p.SetPromptCache(PromptCachePolicy{Mode: PromptCacheExplicit})

	body, err := p.requestBody(types.ChatRequest{
		Messages: []types.Message{{Role: types.RoleUser, Content: "hi"}},
		Tools:    []types.ToolDef{{Name: "bash", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("requestBody: %v", err)
	}
	var wire struct {
		System     []map[string]any `json:"system"`
		ToolConfig struct {
			Tools []map[string]any `json:"tools"`
		} `json:"toolConfig"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wire.System) != 0 {
		t.Fatalf("system = %v, want none", wire.System)
	}
	if len(wire.ToolConfig.Tools) != 2 {
		t.Fatalf("tools = %v, want the tool and its cachePoint", wire.ToolConfig.Tools)
	}
	last := wire.ToolConfig.Tools[len(wire.ToolConfig.Tools)-1]
	cp, ok := last["cachePoint"].(map[string]any)
	if !ok || cp["type"] != "default" {
		t.Fatalf("cachePoint = %v", last)
	}

	// With neither a system block nor tools there is no prefix to mark.
	p.SetPromptCache(PromptCachePolicy{Mode: PromptCacheOff})
	body, err = p.requestBody(types.ChatRequest{
		Messages: []types.Message{{Role: types.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("requestBody: %v", err)
	}
	if strings.Contains(string(body), "cachePoint") {
		t.Errorf("an off policy sent a cachePoint: %s", body)
	}
}

// TestBedrockSignatureCoversBodyAndSessionToken: the payload hash is part of the
// signature, and a session token joins the signed headers.
func TestBedrockSignatures(t *testing.T) {
	var auths []string
	var dates []string
	var tokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		dates = append(dates, r.Header.Get("X-Amz-Date"))
		tokens = append(tokens, r.Header.Get("X-Amz-Security-Token"))
		io.WriteString(w, `{"output":{"message":{"content":[{"text":"ok"}]}},"usage":{"inputTokens":1,"outputTokens":1}}`)
	}))
	defer srv.Close()

	fixed := func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }
	p := NewBedrockProvider("AKID", "SECRET", "SESSION", "eu-west-1", srv.URL, "m")
	p.now = fixed

	for _, prompt := range []string{"first", "second"} {
		if _, err := p.Chat(context.Background(), types.ChatRequest{
			Messages: []types.Message{{Role: types.RoleUser, Content: prompt}},
		}); err != nil {
			t.Fatalf("Chat: %v", err)
		}
	}
	if len(auths) != 2 {
		t.Fatalf("requests = %d", len(auths))
	}
	for _, date := range dates {
		if date != "20261024T120000Z" {
			t.Errorf("X-Amz-Date = %q", date)
		}
	}
	for _, token := range tokens {
		if token != "SESSION" {
			t.Errorf("X-Amz-Security-Token = %q", token)
		}
	}
	if !strings.Contains(auths[0], "SignedHeaders=host;x-amz-date;x-amz-security-token") {
		t.Errorf("session token was not a signed header: %q", auths[0])
	}
	if !strings.Contains(auths[0], "/eu-west-1/bedrock/aws4_request") {
		t.Errorf("credential scope did not carry the region and service: %q", auths[0])
	}
	if auths[0] == auths[1] {
		t.Error("two different bodies produced the same signature")
	}
	signature := auths[0][strings.Index(auths[0], "Signature=")+len("Signature="):]
	if len(signature) != 64 {
		t.Errorf("signature is not 64 hex characters: %q", signature)
	}
}

// TestBedrockConfigErrors: missing credentials or a region fail before a request
// is sent and are not retried.
func TestBedrockConfigErrors(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer srv.Close()

	p := NewBedrockProvider("", "", "", "", srv.URL, "m")
	if _, err := p.Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{{Role: types.RoleUser, Content: "hi"}},
	}); err == nil || !strings.Contains(err.Error(), "bedrock config") {
		t.Fatalf("missing credentials error = %v", err)
	}

	p = NewBedrockProvider("AKID", "SECRET", "", "", srv.URL, "m")
	if _, err := p.Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{{Role: types.RoleUser, Content: "hi"}},
	}); err == nil || !strings.Contains(err.Error(), "no region") {
		t.Fatalf("missing region error = %v", err)
	}
	if requests != 0 {
		t.Errorf("a configuration error sent %d requests", requests)
	}
}

// TestBedrockStreamException surfaces an exception frame instead of returning a
// partial answer.
func TestBedrockStreamException(t *testing.T) {
	frame := bedrockFrame(t, map[string]string{
		":message-type":   "exception",
		":exception-type": "throttlingException",
	}, `{"message":"slow down"}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(frame)
	}))
	defer srv.Close()

	p := NewBedrockProvider("AKID", "SECRET", "", "us-east-1", srv.URL, "m")
	_, err := p.Chat(context.Background(), types.ChatRequest{
		Messages:        []types.Message{{Role: types.RoleUser, Content: "hi"}},
		StreamCallbacks: &types.StreamCallbacks{OnTextDelta: func(string) {}},
	})
	if err == nil || !strings.Contains(err.Error(), "throttlingException") {
		t.Fatalf("exception frame = %v", err)
	}
}

// TestBedrockCorruptFrame refuses a stream whose CRC does not match.
func TestBedrockCorruptFrame(t *testing.T) {
	frame := bedrockEvent(t, "contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"hi"}}`)
	frame[len(frame)-1] ^= 0xff
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(frame)
	}))
	defer srv.Close()

	p := NewBedrockProvider("AKID", "SECRET", "", "us-east-1", srv.URL, "m")
	_, err := p.Chat(context.Background(), types.ChatRequest{
		Messages:        []types.Message{{Role: types.RoleUser, Content: "hi"}},
		StreamCallbacks: &types.StreamCallbacks{OnTextDelta: func(string) {}},
	})
	if err == nil || !strings.Contains(err.Error(), "corrupt message CRC") {
		t.Fatalf("corrupt frame = %v", err)
	}
}
