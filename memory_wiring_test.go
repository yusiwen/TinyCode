package main

import (
	"os"
	"path/filepath"
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
