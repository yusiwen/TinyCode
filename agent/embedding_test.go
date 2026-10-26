package agent

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/config"
)

// withStubEmbeddingClient points the package client at the test server for the
// duration of one test, the same way the memory adapter's tests do.
func withStubEmbeddingClient(t *testing.T, srv *httptest.Server) {
	t.Helper()
	prev := embedHTTP
	embedHTTP = srv.Client()
	t.Cleanup(func() { embedHTTP = prev })
}

// TestOpenAIEmbedderAlignsByIndex pins the OpenAI shape: the path, the bearer
// key, the batch body, and the fact that a server answering out of order cannot
// pair a vector with the wrong fact.
func TestOpenAIEmbedderAlignsByIndex(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		// Answered in reverse order on purpose.
		io.WriteString(w, `{"data":[{"index":1,"embedding":[0,3]},{"index":0,"embedding":[3,0]}]}`)
	}))
	defer srv.Close()
	withStubEmbeddingClient(t, srv)

	e := NewOpenAIEmbedder(srv.URL+"/v1", "text-embedding-3-small", "secret")
	vecs, err := e.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if gotPath != "/v1/embeddings" {
		t.Fatalf("path = %q, want /v1/embeddings", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBody["model"] != "text-embedding-3-small" {
		t.Fatalf("body = %+v", gotBody)
	}
	if input, ok := gotBody["input"].([]any); !ok || len(input) != 2 {
		t.Fatalf("input = %+v, want both texts", gotBody["input"])
	}
	if len(vecs) != 2 || vecs[0][0] != 3 || vecs[1][1] != 3 {
		t.Fatalf("vectors came back misaligned: %+v", vecs)
	}
}

// TestOllamaEmbedderUsesTheBatchEndpoint pins the other shape: Ollama's
// /api/embed and its {"embeddings": [[...]]} envelope.
func TestOllamaEmbedderUsesTheBatchEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("a keyless local endpoint was sent %q", auth)
		}
		io.WriteString(w, `{"embeddings":[[1,0,0],[0,1,0]]}`)
	}))
	defer srv.Close()
	withStubEmbeddingClient(t, srv)

	e := NewOllamaEmbedder(srv.URL, "nomic-embed-text")
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if gotPath != "/api/embed" {
		t.Fatalf("path = %q, want /api/embed", gotPath)
	}
	if len(vecs) != 2 || vecs[1][1] != 1 {
		t.Fatalf("vectors = %+v", vecs)
	}
}

// TestEmbedderReportsWhatWentWrong pins the failure surface a run depends on:
// every one of these is an error the store turns into a lexical fallback.
func TestEmbedderReportsWhatWentWrong(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer failing.Close()
	short := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"index":0,"embedding":[1]}]}`)
	}))
	defer short.Close()
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[]}`)
	}))
	defer empty.Close()
	withStubEmbeddingClient(t, failing)

	if _, err := NewOllamaEmbedder(failing.URL, "m").Embed(context.Background(), []string{"x"}); err == nil ||
		!strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "model not found") {
		t.Errorf("a 404 with a body: error = %v", err)
	}
	if _, err := NewOpenAIEmbedder(short.URL, "m", "").Embed(context.Background(), []string{"x", "y"}); err == nil ||
		!strings.Contains(err.Error(), "asked for 2") {
		t.Errorf("a short batch: error = %v", err)
	}
	if _, err := NewOpenAIEmbedder(empty.URL, "m", "").Embed(context.Background(), []string{"x"}); err == nil {
		t.Error("an empty data array was accepted")
	}
	if _, err := NewOllamaEmbedder(short.URL, "m").Embed(context.Background(), nil); err != nil {
		t.Errorf("an empty batch should be a no-op: %v", err)
	}
	if _, err := NewOllamaEmbedder(short.URL, "m").Embed(context.Background(), []string{"  "}); err == nil {
		t.Error("a blank text was sent to the endpoint")
	}
}

// TestCosineAndNormalize pins the arithmetic the ranking rests on: normalised
// vectors score themselves 1, opposite vectors score -1, and a vector of another
// dimension scores 0 rather than a number computed over the wrong axis.
func TestCosineAndNormalize(t *testing.T) {
	unit := normalize([]float32{3, 4})
	if math.Abs(float64(unit[0])-0.6) > 1e-6 || math.Abs(float64(unit[1])-0.8) > 1e-6 {
		t.Fatalf("normalize([3 4]) = %v", unit)
	}
	if got := cosine(unit, unit); math.Abs(got-1) > 1e-6 {
		t.Fatalf("cosine(v, v) = %v", got)
	}
	if got := cosine(unit, normalize([]float32{-3, -4})); math.Abs(got+1) > 1e-6 {
		t.Fatalf("cosine(v, -v) = %v", got)
	}
	if got := cosine(unit, []float32{1, 0, 0}); got != 0 {
		t.Fatalf("a dimension mismatch scored %v, want 0", got)
	}
	if got := cosine(nil, unit); got != 0 {
		t.Fatalf("an empty vector scored %v, want 0", got)
	}
}

// TestVectorRecordRoundTrip pins the sidecar's encoding: unit vectors survive
// packed base64, and a truncated record is an error rather than a shifted vector.
func TestVectorRecordRoundTrip(t *testing.T) {
	vec := normalize([]float32{1, 2, 3, 4})
	encoded := encodeVector(vec)
	decoded, err := decodeVector(encoded, len(vec))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded) != len(vec) {
		t.Fatalf("decoded %d dims, want %d", len(decoded), len(vec))
	}
	for i := range vec {
		if math.Abs(float64(decoded[i]-vec[i])) > 1e-6 {
			t.Fatalf("dimension %d came back as %v, want %v", i, decoded[i], vec[i])
		}
	}
	if _, err := decodeVector(encoded, len(vec)+1); err == nil {
		t.Fatal("a dimension mismatch was accepted")
	}
}

// TestEmbeddingDefaultsMatchConfig pins the two layers' numbers, the same way
// the store's bounds are pinned.
func TestEmbeddingDefaultsMatchConfig(t *testing.T) {
	if defaultSemanticMinScore != config.DefaultEmbeddingMinScore {
		t.Fatalf("min score = %v, config = %v", defaultSemanticMinScore, config.DefaultEmbeddingMinScore)
	}
	if defaultSemanticWeight != config.DefaultEmbeddingSemanticWeight {
		t.Fatalf("semantic weight = %v, config = %v", defaultSemanticWeight, config.DefaultEmbeddingSemanticWeight)
	}
	if defaultLexicalWeight != config.DefaultEmbeddingLexicalWeight {
		t.Fatalf("lexical weight = %v, config = %v", defaultLexicalWeight, config.DefaultEmbeddingLexicalWeight)
	}
	if defaultBackfill != config.DefaultEmbeddingBackfill {
		t.Fatalf("backfill = %d, config = %d", defaultBackfill, config.DefaultEmbeddingBackfill)
	}
}
