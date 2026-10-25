package tool

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/yusiwen/tinycode/types"
)

// The memory store is not a tool path.
//
// It holds what a person chose to remember — the facts every later run is
// allowed to recall — and a fact the agent can rewrite is not one: a model that
// could edit the store could seed the next session with anything, and one that
// could delete it could erase the correction it was told to keep. The checkpoint
// and trajectory stores are refused for the same family of reasons, and the
// wording is kept parallel so a reader who has seen one has seen all three.
//
// Full access is the one mode that bypasses it, because that mode means no
// boundary at all and quietly fencing one path inside it would make the mode
// mean something narrower than it says.
var (
	memoryMu   sync.Mutex
	memoryRoot string
)

// SetMemoryRoot names the directory the memory store lives in, so the fence can
// refuse it. An empty string turns the guard off, which is what every test and
// every run with memory disabled gets.
func SetMemoryRoot(dir string) {
	memoryMu.Lock()
	defer memoryMu.Unlock()
	memoryRoot = dir
}

// MemoryRoot reports the directory the memory store lives in, or "" when memory
// is off or reads somewhere that needs no guard.
func MemoryRoot() string {
	memoryMu.Lock()
	defer memoryMu.Unlock()
	return memoryRoot
}

// memoryStoreRefusal refuses tool access to the store. It is consulted by
// CheckPathAccess, before the path fence, so a grant that happens to cover the
// directory cannot open it either.
func memoryStoreRefusal(ctx context.Context, path string) string {
	store := MemoryRoot()
	if store == "" {
		return ""
	}
	policy := runPolicy(ctx)
	if policy.Mode == types.SandboxFullAccess {
		return ""
	}
	realStore := filepath.Clean(resolveRealPath(absoluteNoClean(store)))
	if !beneathRoot(realStore, filepath.Clean(resolveRealPath(absoluteNoClean(path)))) {
		return ""
	}
	return types.Refusal{
		Subject: fmt.Sprintf("path %q", path),
		Reason:  "is inside the memory store, which holds what the user chose to remember",
		Mode:    policy.Mode,
		Ask:     types.AskReserved,
		Detail:  []string{"store: " + store},
	}.Message()
}
