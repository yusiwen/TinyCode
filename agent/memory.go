package agent

import "github.com/yusiwen/tinycode/types"

// HTTPMemoryClient adapts any REST API as a MemoryStore.
// Point BaseURL at Hindsight, Honcho, or your own service.
type HTTPMemoryClient struct {
	BaseURL string
}

func NewHTTPMemoryClient(baseURL string) *HTTPMemoryClient {
	return &HTTPMemoryClient{BaseURL: baseURL}
}

func (c *HTTPMemoryClient) Remember(key, value string) error {
	// POST /remember
	panic("TODO: implement HTTP adapter")
}

func (c *HTTPMemoryClient) Recall(query string, limit int) ([]types.Memory, error) {
	// GET /recall?q=...
	panic("TODO: implement HTTP adapter")
}

func (c *HTTPMemoryClient) Forget(key string) error {
	// DELETE /forget/{key}
	panic("TODO: implement HTTP adapter")
}

func (c *HTTPMemoryClient) List() ([]types.Memory, error) {
	// GET /list
	panic("TODO: implement HTTP adapter")
}
