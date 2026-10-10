package agent

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// ─── S1: the cache-tier usage detail ───────────────────────────

// TestUsageDetailFromEitherShape pins the normalization: the same lane arrives
// under two wire names depending on the route, and both have to land in one
// field. A cache-hit call and a cache-miss call with identical totals must not
// be the same record, which is what the old Usage could not express.
func TestUsageDetailFromEitherShape(t *testing.T) {
	cases := []struct {
		name  string
		usage string
		want  types.Usage
	}{
		{
			name:  "openai and openrouter nested shape",
			usage: `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":60,"cache_write_tokens":10},"completion_tokens_details":{"reasoning_tokens":8}}`,
			want: types.Usage{
				PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
				CachedPromptTokens: 60, CacheWriteTokens: 10, ReasoningTokens: 8,
			},
		},
		{
			name:  "deepseek flat shape",
			usage: `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_cache_hit_tokens":60,"prompt_cache_miss_tokens":40}`,
			want: types.Usage{
				PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
				CachedPromptTokens: 60,
			},
		},
		{
			name:  "no detail at all",
			usage: `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}`,
			want:  types.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":` + tc.usage + `}`
			res, err := (&OpenAIProvider{model: "m"}).chatBatch(context.Background(),
				io.NopCloser(strings.NewReader(body)), time.Now())
			if err != nil {
				t.Fatalf("chatBatch: %v", err)
			}
			if res.Usage == nil {
				t.Fatal("usage was dropped")
			}
			if *res.Usage != tc.want {
				t.Errorf("usage = %+v, want %+v", *res.Usage, tc.want)
			}
		})
	}
}

// TestUsageDetailPrefersTheNestedShape: a route that sends both must not have its
// cache tokens counted twice, so the nested object wins and the flat fields are
// ignored rather than added.
func TestUsageDetailPrefersTheNestedShape(t *testing.T) {
	body := `{"choices":[{"message":{"content":"hi"}}],"usage":{` +
		`"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,` +
		`"prompt_tokens_details":{"cached_tokens":60},"prompt_cache_hit_tokens":99}}`
	res, err := (&OpenAIProvider{model: "m"}).chatBatch(context.Background(),
		io.NopCloser(strings.NewReader(body)), time.Now())
	if err != nil {
		t.Fatalf("chatBatch: %v", err)
	}
	if res.Usage.CachedPromptTokens != 60 {
		t.Errorf("cached = %d, want the nested 60 (the flat 99 must not win or add)",
			res.Usage.CachedPromptTokens)
	}
}

// TestUsageDetailFromTheStreamingTail covers the same parsing on the streaming
// path, where the usage arrives in its own chunk after finish_reason.
func TestUsageDetailFromTheStreamingTail(t *testing.T) {
	input := "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":194,\"completion_tokens\":2,\"total_tokens\":196,\"prompt_tokens_details\":{\"cached_tokens\":100,\"cache_write_tokens\":94},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n" +
		"data: [DONE]\n"
	res, err := (&OpenAIProvider{model: "m"}).chatStream(context.Background(),
		io.NopCloser(strings.NewReader(input)), time.Now(), &types.StreamCallbacks{}, nil)
	if err != nil {
		t.Fatalf("chatStream: %v", err)
	}
	want := types.Usage{
		PromptTokens: 194, CompletionTokens: 2, TotalTokens: 196,
		CachedPromptTokens: 100, CacheWriteTokens: 94, ReasoningTokens: 1,
	}
	if res.Usage == nil || *res.Usage != want {
		t.Fatalf("usage = %+v, want %+v", res.Usage, want)
	}
}

// TestUsageAddKeepsEveryLane guards the accumulator: a lane that Add forgot
// would silently vanish from a session total.
func TestUsageAddKeepsEveryLane(t *testing.T) {
	a := types.Usage{
		PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3,
		CachedPromptTokens: 4, CacheWriteTokens: 5, ReasoningTokens: 6,
	}
	b := types.Usage{
		PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30,
		CachedPromptTokens: 40, CacheWriteTokens: 50, ReasoningTokens: 60,
	}
	want := types.Usage{
		PromptTokens: 11, CompletionTokens: 22, TotalTokens: 33,
		CachedPromptTokens: 44, CacheWriteTokens: 55, ReasoningTokens: 66,
	}
	if got := a.Add(b); got != want {
		t.Errorf("Add = %+v, want %+v", got, want)
	}
}
