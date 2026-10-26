package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// bucketEmbedder is a stand-in for an embedding model: it maps a handful of
// synonyms onto one axis, so two texts that share no word can still be close.
// That is exactly the property the lexical store lacks and the reason this code
// exists; it is a fake model, not a claim about any real one's quality.
type bucketEmbedder struct {
	model string
	err   error
	calls int
}

func (b *bucketEmbedder) Model() string {
	if b.model == "" {
		return "bucket-v1"
	}
	return b.model
}

func (b *bucketEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = bucketVector(text)
	}
	return out, nil
}

// bucketWords are the synonym sets the fake model knows. A text carrying any
// word of a set lights that axis.
var bucketWords = [][]string{
	{"test", "tests", "验证", "测试", "verify"},
	{"deploy", "release", "上线", "发布", "ship"},
	{"editor", "vim", "neovim", "编辑器"},
}

func bucketVector(text string) []float32 {
	lower := strings.ToLower(text)
	vec := make([]float32, len(bucketWords))
	for axis, words := range bucketWords {
		for _, word := range words {
			if strings.Contains(lower, word) {
				vec[axis] = 1
				break
			}
		}
	}
	return vec
}

// TestSemanticRecallFindsWhatLexicalCannot is the entry's whole point: a query
// and a fact that share no token, where the store's own ranking returns nothing.
func TestSemanticRecallFindsWhatLexicalCannot(t *testing.T) {
	inner := newTestStore(t, t.TempDir(), "proj")
	embedder := &bucketEmbedder{}
	sem, err := NewSemanticMemory(inner, embedder, SemanticOptions{})
	if err != nil {
		t.Fatalf("open semantic store: %v", err)
	}
	if err := sem.Remember("测试命令", "go test ./... with HOME redirected"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if err := sem.Remember("上线流程", "tag the commit, then push the tag"); err != nil {
		t.Fatalf("remember: %v", err)
	}

	// The lexical store alone: the query's bigrams (如何, 何验, 验证) appear in
	// neither fact, so it has nothing to offer.
	lexical, err := inner.Recall(context.Background(), "如何验证", 5)
	if err != nil {
		t.Fatalf("lexical recall: %v", err)
	}
	if len(lexical) != 0 {
		t.Fatalf("the lexical store matched after all, so this test proves nothing: %+v", lexical)
	}

	hits, err := sem.Recall(context.Background(), "如何验证", 5)
	if err != nil {
		t.Fatalf("semantic recall: %v", err)
	}
	if len(hits) == 0 || hits[0].Key != "测试命令" {
		t.Fatalf("semantic recall = %+v, want the test fact first", hits)
	}
	for _, hit := range hits {
		if hit.Key == "上线流程" {
			t.Fatalf("a fact on another axis was returned: %+v", hits)
		}
	}
}

// TestSemanticRecallFallsBackWhenTheEndpointFails pins the failure direction: a
// dead embedding server costs recall quality, never the run.
func TestSemanticRecallFallsBackWhenTheEndpointFails(t *testing.T) {
	inner := newTestStore(t, t.TempDir(), "proj")
	embedder := &bucketEmbedder{err: errors.New("endpoint down")}
	sem, err := NewSemanticMemory(inner, embedder, SemanticOptions{})
	if err != nil {
		t.Fatalf("open semantic store: %v", err)
	}
	if err := sem.Remember("deploy", "staging takes ten minutes"); err != nil {
		t.Fatalf("a failed embedding lost the fact: %v", err)
	}

	hits, err := sem.Recall(context.Background(), "deploy", 5)
	if err != nil {
		t.Fatalf("recall returned an error instead of falling back: %v", err)
	}
	if len(hits) != 1 || hits[0].Key != "deploy" {
		t.Fatalf("the lexical fallback returned %+v", hits)
	}
}

// TestSemanticRecallBackfillsFactsWrittenBeforeIt pins the lazy migration: facts
// written while embeddings were off are embedded by the next recall, in one
// bounded batch, and only once.
func TestSemanticRecallBackfillsFactsWrittenBeforeIt(t *testing.T) {
	inner := newTestStore(t, t.TempDir(), "proj")
	if err := inner.Remember("测试命令", "go test ./... with HOME redirected"); err != nil {
		t.Fatalf("remember: %v", err)
	}

	embedder := &bucketEmbedder{}
	sem, err := NewSemanticMemory(inner, embedder, SemanticOptions{})
	if err != nil {
		t.Fatalf("open semantic store: %v", err)
	}

	hits, err := sem.Recall(context.Background(), "如何验证", 5)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) == 0 || hits[0].Key != "测试命令" {
		t.Fatalf("a backfilled fact was not recalled: %+v", hits)
	}
	// One batch for the fact, one for the query.
	if embedder.calls != 2 {
		t.Fatalf("first recall made %d embedding calls, want the batch and the query", embedder.calls)
	}
	before := embedder.calls
	if _, err := sem.Recall(context.Background(), "如何验证", 5); err != nil {
		t.Fatalf("second recall: %v", err)
	}
	if embedder.calls != before+1 {
		t.Fatalf("the second recall re-embedded: %d calls after %d", embedder.calls, before)
	}
}

// TestSemanticRecallDropsVectorsFromAnotherModel pins what a model change means:
// the old vectors are not compared against the new ones, the facts stay, and the
// next recall re-embeds them.
func TestSemanticRecallDropsVectorsFromAnotherModel(t *testing.T) {
	inner := newTestStore(t, t.TempDir(), "proj")
	first, err := NewSemanticMemory(inner, &bucketEmbedder{model: "m1"}, SemanticOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := first.Remember("测试命令", "go test ./... with HOME redirected"); err != nil {
		t.Fatalf("remember: %v", err)
	}

	second, err := NewSemanticMemory(inner, &bucketEmbedder{model: "m2"}, SemanticOptions{})
	if err != nil {
		t.Fatalf("reopen with another model: %v", err)
	}
	raw, err := os.ReadFile(second.Path())
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if strings.Contains(string(raw), `"m1"`) {
		t.Fatalf("the old model's vectors survived the reopen:\n%s", raw)
	}
	if facts, err := inner.List(); err != nil || len(facts) != 1 {
		t.Fatalf("the fact did not survive the model change: %+v, %v", facts, err)
	}

	hits, err := second.Recall(context.Background(), "如何验证", 5)
	if err != nil {
		t.Fatalf("recall after the model change: %v", err)
	}
	if len(hits) == 0 || hits[0].Key != "测试命令" {
		t.Fatalf("the fact was not re-embedded and recalled: %+v", hits)
	}
	raw, _ = os.ReadFile(second.Path())
	if !strings.Contains(string(raw), `"m2"`) {
		t.Fatalf("the new model's vector was not stored:\n%s", raw)
	}
}

// TestSemanticForgetRemovesTheVectorToo pins that deleting a fact deletes its
// vector, so a later fact with the same text cannot inherit one.
func TestSemanticForgetRemovesTheVectorToo(t *testing.T) {
	inner := newTestStore(t, t.TempDir(), "proj")
	sem, err := NewSemanticMemory(inner, &bucketEmbedder{}, SemanticOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sem.Remember("测试命令", "go test ./..."); err != nil {
		t.Fatalf("remember: %v", err)
	}
	facts, _ := sem.List()
	if len(facts) != 1 {
		t.Fatalf("facts = %+v", facts)
	}
	if err := sem.Forget(facts[0].ID); err != nil {
		t.Fatalf("forget: %v", err)
	}
	raw, err := os.ReadFile(sem.Path())
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if strings.Contains(string(raw), facts[0].ID) {
		t.Fatalf("the vector outlived the fact:\n%s", raw)
	}
}

// TestSemanticRecallRespectsTheFloor is the control for the score threshold: a
// fact on an unrelated axis is not returned just because it has a vector.
func TestSemanticRecallRespectsTheFloor(t *testing.T) {
	inner := newTestStore(t, t.TempDir(), "proj")
	sem, err := NewSemanticMemory(inner, &bucketEmbedder{}, SemanticOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sem.Remember("上线流程", "tag the commit, then push the tag"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	hits, err := sem.Recall(context.Background(), "如何验证", 5)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("an unrelated fact cleared the floor: %+v", hits)
	}
}

// TestSemanticRememberWithoutALabel pins the id the vector is filed under: the
// store labels an unlabelled fact "fact", and a vector filed under the empty key
// would never be matched to it again.
func TestSemanticRememberWithoutALabel(t *testing.T) {
	inner := newTestStore(t, t.TempDir(), "proj")
	sem, err := NewSemanticMemory(inner, &bucketEmbedder{}, SemanticOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sem.Remember("", "go test ./... with HOME redirected"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	facts, _ := sem.List()
	if len(facts) != 1 || facts[0].Key != "fact" {
		t.Fatalf("facts = %+v", facts)
	}
	sem.mu.Lock()
	_, hasVector := sem.vectors[facts[0].ID]
	sem.mu.Unlock()
	if !hasVector {
		t.Fatalf("the vector was filed under an id the fact does not have: %v", sem.vectors)
	}
}
