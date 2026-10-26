package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Embedder turns text into vectors (roadmap B3).
//
// Model is part of the contract rather than a detail: vectors from two models
// are not comparable, so the model's name travels with every vector the store
// keeps and a change invalidates the old ones instead of mixing them.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Model() string
}

// embedHTTP is the client every embedding request goes through. A local Ollama
// and a hosted endpoint both answer in well under this bound; a request that
// does not has failed as far as a run is concerned, because recall must not hold
// a turn open (the store falls back to its lexical ranking).
var embedHTTP = &http.Client{Timeout: 20 * time.Second}

// Embedding endpoint shapes. The two differ in the path and in the response
// envelope, not in what they do.
type embedShape int

const (
	embedShapeOpenAI embedShape = iota // POST {base}/embeddings -> {"data":[{"embedding":[...]}]}
	embedShapeOllama                   // POST {base}/api/embed  -> {"embeddings":[[...]]}
)

// HTTPEmbedder is the Embedder for the two request shapes this repository
// speaks: any OpenAI-compatible /embeddings endpoint, and Ollama's /api/embed.
// It is deliberately not routed through the chat providers: an embedding model
// is its own endpoint and its own model name, and a chat provider that happens
// to be selected must not silently answer embeddings.
type HTTPEmbedder struct {
	shape   embedShape
	baseURL string
	model   string
	apiKey  string
}

// NewOpenAIEmbedder builds an embedder for an OpenAI-compatible /embeddings
// endpoint. An empty key sends no Authorization header, which is what a local
// server that needs none expects.
func NewOpenAIEmbedder(baseURL, model, apiKey string) *HTTPEmbedder {
	return &HTTPEmbedder{shape: embedShapeOpenAI, baseURL: baseURL, model: model, apiKey: apiKey}
}

// NewOllamaEmbedder builds an embedder for a local Ollama server.
func NewOllamaEmbedder(baseURL, model string) *HTTPEmbedder {
	return &HTTPEmbedder{shape: embedShapeOllama, baseURL: baseURL, model: model}
}

// Model is the embedding model's name, stored beside every vector.
func (e *HTTPEmbedder) Model() string { return e.model }

// Embed turns each text into a vector. The result is index-aligned with texts:
// a server that answers out of order is reordered, because the caller pairs a
// vector with the fact it came from.
func (e *HTTPEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	for i, text := range texts {
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("embedding: text %d is empty", i)
		}
	}

	path := "/embeddings"
	if e.shape == embedShapeOllama {
		path = "/api/embed"
	}
	body, err := json.Marshal(map[string]any{"model": e.model, "input": texts})
	if err != nil {
		return nil, fmt.Errorf("embedding: encode: %w", err)
	}

	url := strings.TrimRight(e.baseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedding: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := embedHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("embedding: read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("embedding: %s -> %d: %s", url, resp.StatusCode, clipForError(raw))
	}

	if e.shape == embedShapeOllama {
		return decodeOllamaEmbeddings(raw, len(texts))
	}
	return decodeOpenAIEmbeddings(raw, len(texts))
}

// decodeOpenAIEmbeddings reads {"data":[{"index":n,"embedding":[...]}]}.
func decodeOpenAIEmbeddings(raw []byte, want int) ([][]float32, error) {
	var payload struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("embedding: decode: %w", err)
	}
	if len(payload.Data) != want {
		return nil, fmt.Errorf("embedding: asked for %d vectors, got %d", want, len(payload.Data))
	}
	out := make([][]float32, want)
	for _, item := range payload.Data {
		if item.Index < 0 || item.Index >= want {
			return nil, fmt.Errorf("embedding: vector index %d out of range", item.Index)
		}
		if len(item.Embedding) == 0 {
			return nil, fmt.Errorf("embedding: vector %d is empty", item.Index)
		}
		out[item.Index] = item.Embedding
	}
	return out, nil
}

// decodeOllamaEmbeddings reads {"embeddings":[[...]]}.
func decodeOllamaEmbeddings(raw []byte, want int) ([][]float32, error) {
	var payload struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("embedding: decode: %w", err)
	}
	if len(payload.Embeddings) != want {
		return nil, fmt.Errorf("embedding: asked for %d vectors, got %d", want, len(payload.Embeddings))
	}
	for i, vec := range payload.Embeddings {
		if len(vec) == 0 {
			return nil, fmt.Errorf("embedding: vector %d is empty", i)
		}
	}
	return payload.Embeddings, nil
}

// clipForError keeps an error body short: a proxy's HTML error page is not
// something to paste into a terminal.
func clipForError(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > 200 {
		return text[:200] + "…"
	}
	return text
}

// cosine returns the similarity of two unit vectors, or 0 when either is empty
// or their dimensions differ — a mismatched vector is a fact that cannot be
// compared, so it scores as no match instead of as a random one.
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot
}

// normalize returns the vector scaled to unit length, so a later cosine is a dot
// product. A zero vector stays zero; it can never match anything.
func normalize(vec []float32) []float32 {
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	if sum == 0 {
		return vec
	}
	scale := 1 / math.Sqrt(sum)
	out := make([]float32, len(vec))
	for i, v := range vec {
		out[i] = float32(float64(v) * scale)
	}
	return out
}
