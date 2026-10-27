package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
)

// TestMemoryStoreBuildsTheConfiguredBackend pins what the composition root does
// with a resolved section: a local spec becomes a file store, one shard per
// project, and a network spec becomes the adapter pointed at the endpoint the
// user wrote — there is no default endpoint to fall back to.
func TestMemoryStoreBuildsTheConfiguredBackend(t *testing.T) {
	dir := t.TempDir()
	local := config.MemorySpec{Enabled: true, Backend: config.MemoryBackendLocal, Dir: dir, MaxEntries: 10, MaxBytes: 4096}
	store, err := memoryStore(local, "/repo/a")
	if err != nil {
		t.Fatalf("local store: %v", err)
	}
	localStore, ok := store.(*agent.LocalMemory)
	if !ok {
		t.Fatalf("a local spec built %T", store)
	}
	if filepath.Dir(localStore.Path()) != dir {
		t.Fatalf("store path %q is not under %q", localStore.Path(), dir)
	}

	// Two projects sharing the directory must not share the file.
	other, err := memoryStore(local, "/repo/b")
	if err != nil {
		t.Fatalf("second local store: %v", err)
	}
	if other.(*agent.LocalMemory).Path() == localStore.Path() {
		t.Fatal("two projects share one store file")
	}

	remote := config.MemorySpec{Enabled: true, Backend: config.MemoryBackendHindsight, BaseURL: "http://127.0.0.1:9070"}
	store, err = memoryStore(remote, "")
	if err != nil {
		t.Fatalf("remote store: %v", err)
	}
	client, ok := store.(*agent.HTTPMemoryClient)
	if !ok {
		t.Fatalf("a network spec built %T", store)
	}
	if client.BaseURL != remote.BaseURL {
		t.Fatalf("client base = %q, want %q", client.BaseURL, remote.BaseURL)
	}
	if client.Bank != "default" {
		t.Fatalf("bank = %q, want the service default", client.Bank)
	}

	remote.Bank = "work"
	if store, err = memoryStore(remote, ""); err != nil {
		t.Fatalf("named bank: %v", err)
	}
	if got := store.(*agent.HTTPMemoryClient).Bank; got != "work" {
		t.Fatalf("bank = %q, want work", got)
	}
}

// TestMemoryStoreRefusesHindsightWithoutAnEndpoint pins the fix for the silently
// defaulted endpoint: a network backend that names no base_url is an error, so
// the caller logs it and runs without memory rather than sending the prompt to
// a service nobody configured.
func TestMemoryStoreRefusesHindsightWithoutAnEndpoint(t *testing.T) {
	spec := config.MemorySpec{Enabled: true, Backend: config.MemoryBackendHindsight}
	store, err := memoryStore(spec, "")
	if err == nil {
		t.Fatalf("a hindsight spec with no base_url built %T", store)
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("the error does not name the missing key: %v", err)
	}
}

// TestMemoryStoreReportsAnUnusableDirectory pins that a store path that cannot
// be created is an error the caller can log and skip, rather than a panic or a
// silently empty store.
func TestMemoryStoreReportsAnUnusableDirectory(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := config.MemorySpec{Enabled: true, Backend: config.MemoryBackendLocal, Dir: filepath.Join(blocker, "memory")}
	if _, err := memoryStore(spec, ""); err == nil {
		t.Fatal("a store under a regular file was accepted")
	}
}

// TestMemoryStoreWrapsLocalWithEmbeddings pins the B3 wiring: an embedding
// section turns the local store into semantic recall, and a section the client
// cannot build falls back to the lexical store rather than failing the start.
func TestMemoryStoreWrapsLocalWithEmbeddings(t *testing.T) {
	spec := config.MemorySpec{
		Enabled:    true,
		Backend:    config.MemoryBackendLocal,
		Dir:        t.TempDir(),
		MaxEntries: 10,
		MaxBytes:   4096,
		Embedding: config.EmbeddingSpec{
			Enabled:  true,
			Provider: config.EmbeddingProviderOllama,
			BaseURL:  "http://127.0.0.1:11434",
			Model:    "nomic-embed-text",
			Backfill: 4,
		},
	}
	store, err := memoryStore(spec, "/repo/a")
	if err != nil {
		t.Fatalf("semantic store: %v", err)
	}
	if _, ok := store.(*agent.SemanticMemory); !ok {
		t.Fatalf("an embedding section built %T, want the semantic wrapper", store)
	}

	spec.Embedding.Provider = "hinsight" // a typo the client cannot build
	store, err = memoryStore(spec, "/repo/a")
	if err != nil {
		t.Fatalf("a bad provider must not fail the store: %v", err)
	}
	if _, ok := store.(*agent.LocalMemory); !ok {
		t.Fatalf("a bad provider built %T, want the plain local store", store)
	}
}
