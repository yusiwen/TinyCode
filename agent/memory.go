package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// ErrMemoryDeleteUnsupported reports a backend with no delete of its own. It is
// an error rather than a quiet nil because the user asked for a fact to be gone
// and it is not (docs/memory-decision.md).
var ErrMemoryDeleteUnsupported = errors.New("memory: this backend cannot delete a fact")

const hindsightBase = "https://memory.yusiwen.cn/dp-api"
const hindsightBank = "hermes"

var hindsightHTTP = &http.Client{Timeout: 15 * time.Second}

type HTTPMemoryClient struct {
	BaseURL string
}

func NewHTTPMemoryClient(baseURL string) *HTTPMemoryClient {
	return &HTTPMemoryClient{BaseURL: baseURL}
}

func (c *HTTPMemoryClient) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return hindsightBase
}

func (c *HTTPMemoryClient) auth() string {
	if v := os.Getenv("HINDSIGHT_API_KEY"); v != "" {
		return v
	}
	return os.Getenv("OPENAI_API_KEY")
}

func (c *HTTPMemoryClient) do(ctx context.Context, method, url string, body, dst any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.auth())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hindsightHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("hindsight: %s %s -> %d: %s", method, url, resp.StatusCode, string(b))
	}
	if dst != nil {
		return json.NewDecoder(resp.Body).Decode(dst)
	}
	return nil
}

func (c *HTTPMemoryClient) Remember(key, value string) error {
	u := c.base() + "/v1/default/banks/" + hindsightBank + "/memories"
	body := map[string]any{"items": []map[string]string{{"content": value, "context": key}}}
	return c.do(context.Background(), http.MethodPost, u, body, nil)
}

func (c *HTTPMemoryClient) Recall(ctx context.Context, query string, limit int) ([]types.Memory, error) {
	u := c.base() + "/v1/default/banks/" + hindsightBank + "/memories/recall"
	body := map[string]any{"query": query, "budget": "mid"}
	var raw struct {
		Results []struct {
			Content string `json:"content"`
			Context string `json:"context"`
		} `json:"results"`
	}
	if err := c.do(ctx, http.MethodPost, u, body, &raw); err != nil {
		return nil, err
	}
	out := make([]types.Memory, 0, len(raw.Results))
	for _, r := range raw.Results {
		out = append(out, types.Memory{Key: r.Context, Value: r.Content})
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (c *HTTPMemoryClient) Forget(id string) error {
	// Hindsight has no per-memory DELETE; curation is a PATCH that invalidates a
	// memory by id, and this adapter does not carry ids yet (issue #293, S4). It
	// reports that rather than returning nil: a delete that silently does nothing
	// is indistinguishable from one that worked.
	return ErrMemoryDeleteUnsupported
}

func (c *HTTPMemoryClient) List() ([]types.Memory, error) {
	u := c.base() + "/v1/default/banks/" + hindsightBank + "/memories/list"
	var raw struct {
		MemoryUnits []struct {
			Content string `json:"content"`
			Context string `json:"context"`
		} `json:"memory_units"`
	}
	if err := c.do(context.Background(), http.MethodGet, u, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]types.Memory, 0, len(raw.MemoryUnits))
	for _, m := range raw.MemoryUnits {
		out = append(out, types.Memory{Key: m.Context, Value: m.Content})
	}
	return out, nil
}
