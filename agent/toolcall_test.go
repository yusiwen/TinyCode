package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// errFailed is the failure the breaker tests repeat.
var errFailed = errors.New("nope")

// TestValidateArgsKeywords pins each implemented keyword with the failure it
// exists to catch. A case that passes with and without the check would be a test
// of nothing, so every entry here has a schema the arguments break.
func TestValidateArgsKeywords(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		args   map[string]any
		want   []string // substrings, all of which must appear
		noWant []string // substrings that must not appear
	}{
		{
			name: "missing required",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"command": map[string]any{"type": "string"}},
				"required":   []string{"command"},
			},
			args: map[string]any{},
			want: []string{`missing required property "command"`},
		},
		{
			name: "wrong type",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"timeout": map[string]any{"type": "number"}},
			},
			args: map[string]any{"timeout": "soon"},
			want: []string{`"timeout" must be number, got string`},
		},
		{
			name: "integer accepts a whole float and rejects a fraction",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"n": map[string]any{"type": "integer"}},
			},
			args: map[string]any{"n": 3.5},
			want: []string{`"n" must be integer, got number`},
		},
		{
			name: "enum",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"status": map[string]any{"type": "string", "enum": []string{"pending", "completed"}},
				},
			},
			args: map[string]any{"status": "done"},
			want: []string{`"status" must be one of "pending", "completed", got "done"`},
		},
		{
			name: "array element type",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"urls": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
			},
			args: map[string]any{"urls": []any{"ok", 7}},
			want: []string{`"urls[1]" must be string, got number`},
		},
		{
			name: "nested object property",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"opts": map[string]any{
						"type":       "object",
						"properties": map[string]any{"depth": map[string]any{"type": "number"}},
					},
				},
			},
			args: map[string]any{"opts": map[string]any{"depth": true}},
			want: []string{`"opts.depth" must be number, got boolean`},
		},
		{
			// The shape tool/todo.go declares: an array of objects with an enum
			// inside, which is why the validator has to recurse.
			name: "through items into properties and enum",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"todos": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"content": map[string]any{"type": "string"},
								"status":  map[string]any{"type": "string", "enum": []string{"pending", "completed"}},
							},
							"required": []string{"content", "status"},
						},
					},
				},
			},
			args: map[string]any{"todos": []any{
				map[string]any{"content": "first", "status": "pending"},
				map[string]any{"status": "done"},
			}},
			want: []string{
				`missing required property "todos[1].content"`,
				`"todos[1].status" must be one of "pending", "completed", got "done"`,
			},
		},
		{
			name: "a satisfied schema reports nothing",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"command": map[string]any{"type": "string"}},
				"required":   []string{"command"},
			},
			args:   map[string]any{"command": "ls"},
			noWant: []string{"must", "missing"},
		},
		{
			name: "an unknown keyword constrains nothing",
			schema: map[string]any{
				"type":                  "object",
				"properties":            map[string]any{"n": map[string]any{"type": "number", "exclusiveMinimum": 10, "multipleOf": 7}},
				"unevaluatedProperties": false,
			},
			args:   map[string]any{"n": 3},
			noWant: []string{"must", "missing"},
		},
		{
			name:   "a tool with no schema rejects nothing",
			schema: nil,
			args:   map[string]any{"anything": []any{1, "two", nil}},
			noWant: []string{"must", "missing"},
		},
		{
			name: "a type list accepts either",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"note": map[string]any{"type": []any{"string", "null"}}},
			},
			args:   map[string]any{"note": nil},
			noWant: []string{"must be"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateArgs(tc.schema, tc.args)
			joined := strings.Join(got, "; ")
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Errorf("problems = %q, want it to contain %q", joined, want)
				}
			}
			for _, unwanted := range tc.noWant {
				if strings.Contains(joined, unwanted) {
					t.Errorf("problems = %q, want it to contain none of %q", joined, unwanted)
				}
			}
		})
	}
}

// TestValidateArgsReportsEveryProblem: a model can fix three mistakes in one step,
// but not three steps' worth of one mistake each.
func TestValidateArgsReportsEveryProblem(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
			"timeout": map[string]any{"type": "number"},
			"mode":    map[string]any{"type": "string", "enum": []string{"fast"}},
		},
		"required": []string{"command"},
	}
	got := ValidateArgs(schema, map[string]any{"timeout": "soon", "mode": "slow"})
	if len(got) != 3 {
		t.Fatalf("problems = %d (%q), want 3", len(got), got)
	}
	// A deterministic order, so two runs report the same problems the same way:
	// required properties first, then the declared ones alphabetically.
	if !strings.Contains(got[0], "command") {
		t.Errorf("problems = %q, want the required property first", got)
	}
	joined := strings.Join(got, "; ")
	if strings.Index(joined, "mode") > strings.Index(joined, "timeout") {
		t.Errorf("problems = %q, want the properties in a stable order", got)
	}
}

// TestToolCallKeyIsOrderIndependent is what makes the breaker recognise a repeat:
// a model that reorders its JSON keys has made the same call.
func TestToolCallKeyIsOrderIndependent(t *testing.T) {
	a := map[string]any{}
	b := map[string]any{}
	if err := json.Unmarshal([]byte(`{"a":1,"b":{"c":2}}`), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"b":{"c":2},"a":1}`), &b); err != nil {
		t.Fatal(err)
	}
	if toolCallKey("bash", a) != toolCallKey("bash", b) {
		t.Error("the same call written two ways produced two identities")
	}
	if toolCallKey("bash", a) == toolCallKey("read_file", a) {
		t.Error("two tools with the same arguments produced one identity")
	}
	c := map[string]any{"a": 2, "b": map[string]any{"c": 2}}
	if toolCallKey("bash", a) == toolCallKey("bash", c) {
		t.Error("different arguments produced one identity")
	}
}

// validatingTool is a tool whose Execute records that it ran, so a test can assert
// the counted fact "the tool was never called".
func validatingTool(schema map[string]any, ran *int) Tool {
	return Tool{
		Name: "bash", Description: "run a command",
		Parameters: schema,
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			*ran++
			return "ran", nil
		},
	}
}

func bashSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"command": map[string]any{"type": "string"}},
		"required":   []string{"command"},
	}
}

// TestInvalidCallIsNotExecuted is the whole point of the issue: a call that breaks
// the schema the model was shown must not reach the tool.
func TestInvalidCallIsNotExecuted(t *testing.T) {
	ran := 0
	step := 0
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			step++
			if step == 1 {
				return &types.ChatResponse{ToolCalls: []types.ToolCall{
					{ID: "c1", Name: "bash", Arguments: `{"timeout": 5}`},
				}}, nil
			}
			return &types.ChatResponse{Content: "done"}, nil
		},
	}
	a := &Agent{
		Provider: provider, MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test",
		Tools: []Tool{validatingTool(bashSchema(), &ran)},
	}

	result, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result != "done" {
		t.Errorf("result = %q, want the run to continue after the rejection", result)
	}
	if ran != 0 {
		t.Errorf("the tool ran %d times, want 0: a call that breaks its schema must not be executed", ran)
	}
}

// TestInvalidArgsMessageReachesTheModel: the model has to be told what is wrong
// with its JSON, in the transcript the next step reads.
func TestInvalidArgsMessageReachesTheModel(t *testing.T) {
	ran := 0
	var seen []string
	step := 0
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			step++
			if step == 1 {
				return &types.ChatResponse{ToolCalls: []types.ToolCall{
					{ID: "c1", Name: "bash", Arguments: `{}`},
				}}, nil
			}
			// The tool result is in the request the second step sees.
			for _, m := range req.Messages {
				if m.Role == types.RoleTool {
					seen = append(seen, m.Content)
				}
			}
			return &types.ChatResponse{Content: "done"}, nil
		},
	}
	a := &Agent{
		Provider: provider, MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test",
		Tools: []Tool{validatingTool(bashSchema(), &ran)},
	}

	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("tool results = %q, want exactly one", seen)
	}
	if !strings.Contains(seen[0], `invalid arguments for bash`) ||
		!strings.Contains(seen[0], `missing required property "command"`) {
		t.Errorf("tool result = %q, want it to name the tool and the missing property", seen[0])
	}
}

// TestRepeatedFailingCallIsRefused: the circuit breaker. Identical call, failing
// every time, is refused once it has failed repeatFailureLimit times.
func TestRepeatedFailingCallIsRefused(t *testing.T) {
	prev := repeatFailureLimit
	repeatFailureLimit = 2
	t.Cleanup(func() { repeatFailureLimit = prev })

	calls := 0
	step := 0
	var toolResults []string
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			step++
			for _, m := range req.Messages {
				if m.Role == types.RoleTool {
					toolResults = append(toolResults, m.Content)
				}
			}
			if step > 6 {
				return &types.ChatResponse{Content: "gave up"}, nil
			}
			// The same failing call every single time.
			return &types.ChatResponse{ToolCalls: []types.ToolCall{
				{ID: "c1", Name: "failer", Arguments: `{"x":1}`},
			}}, nil
		},
	}
	failer := Tool{
		Name: "failer", Description: "always fails",
		Parameters: map[string]any{"type": "object"},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			calls++
			return "", errFailed
		},
	}
	a := &Agent{
		Provider: provider, MaxSteps: 6, MaxTokens: 100, SystemPrompt: "test",
		Tools: []Tool{failer},
	}

	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != repeatFailureLimit {
		t.Errorf("the tool ran %d times, want exactly %d: the loop must be broken after that many failures",
			calls, repeatFailureLimit)
	}
	last := toolResults[len(toolResults)-1]
	if !strings.Contains(last, "[LOOP]") || !strings.Contains(last, "failer") {
		t.Errorf("the final refusal = %q, want it to name the loop and the tool", last)
	}
}

// TestResetPerRunKeepsTheBreakerHonest: the count belongs to one run. A fresh
// prompt is a fresh start, and the model deserves one.
func TestResetPerRunKeepsTheBreakerHonest(t *testing.T) {
	prev := repeatFailureLimit
	repeatFailureLimit = 2
	t.Cleanup(func() { repeatFailureLimit = prev })

	calls := 0
	step := 0
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			step++
			// One failing call per run, then an answer.
			if step%2 == 1 {
				return &types.ChatResponse{ToolCalls: []types.ToolCall{
					{ID: "c1", Name: "failer", Arguments: `{"x":1}`},
				}}, nil
			}
			return &types.ChatResponse{Content: "done"}, nil
		},
	}
	failer := Tool{
		Name: "failer", Description: "always fails",
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			calls++
			return "", errFailed
		},
	}
	a := &Agent{
		Provider: provider, MaxSteps: 3, MaxTokens: 100, SystemPrompt: "test",
		Tools: []Tool{failer},
	}

	for run := 0; run < 3; run++ {
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	if calls != 3 {
		t.Errorf("the tool ran %d times over three runs, want 3: the count must not carry across runs", calls)
	}
}

// TestRepeatedSuccessfulCallIsNotRefused: polling is normal work. Only failures
// count towards the breaker.
func TestRepeatedSuccessfulCallIsNotRefused(t *testing.T) {
	prev := repeatFailureLimit
	repeatFailureLimit = 1
	t.Cleanup(func() { repeatFailureLimit = prev })

	calls := 0
	step := 0
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			step++
			if step > 4 {
				return &types.ChatResponse{Content: "done"}, nil
			}
			// The very same successful call, four times.
			return &types.ChatResponse{ToolCalls: []types.ToolCall{
				{ID: "c1", Name: "poller", Arguments: `{"x":1}`},
			}}, nil
		},
	}
	poller := Tool{
		Name: "poller", Description: "always succeeds",
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			calls++
			return "still running", nil
		},
	}
	a := &Agent{
		Provider: provider, MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test",
		Tools: []Tool{poller},
	}

	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 4 {
		t.Errorf("the tool ran %d times, want 4: a repeated success is work, not a loop", calls)
	}
}

// TestRepeatedInvalidCallIsRefusedAsALoop: the most likely repeat is the one that
// never executed at all, so validation failures feed the breaker too.
func TestRepeatedInvalidCallIsRefusedAsALoop(t *testing.T) {
	prev := repeatFailureLimit
	repeatFailureLimit = 2
	t.Cleanup(func() { repeatFailureLimit = prev })

	ran := 0
	step := 0
	var toolResults []string
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			step++
			for _, m := range req.Messages {
				if m.Role == types.RoleTool {
					toolResults = append(toolResults, m.Content)
				}
			}
			if step > 5 {
				return &types.ChatResponse{Content: "gave up"}, nil
			}
			return &types.ChatResponse{ToolCalls: []types.ToolCall{
				{ID: "c1", Name: "bash", Arguments: `{}`},
			}}, nil
		},
	}
	a := &Agent{
		Provider: provider, MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test",
		Tools: []Tool{validatingTool(bashSchema(), &ran)},
	}

	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ran != 0 {
		t.Errorf("the tool ran %d times, want 0", ran)
	}
	// The first two results explain the schema; from then on the run is told it is
	// looping, which is the more useful fact.
	if !strings.Contains(toolResults[0], "missing required property") {
		t.Errorf("first result = %q, want the schema problem", toolResults[0])
	}
	if !strings.Contains(toolResults[len(toolResults)-1], "[LOOP]") {
		t.Errorf("last result = %q, want the loop refusal", toolResults[len(toolResults)-1])
	}
}
