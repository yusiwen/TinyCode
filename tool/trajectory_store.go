package tool

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/yusiwen/tinycode/types"
)

// The trajectory store is not a tool path.
//
// It holds the record of what a run did, and a record an agent can rewrite is
// not evidence: an evaluation that compared two runs would be comparing two
// documents one of the compared subjects could have written. The checkpoint
// store gets the same treatment for the same reason (a pre-image a tool can edit
// is not an undo), and the refusal is spelled the same way so a reader who has
// seen one has seen both.
//
// Full access is the one mode that bypasses it: that mode means no boundary at
// all, and quietly fencing one path inside it would make the mode mean something
// narrower than it says.
var (
	trajectoryMu   sync.Mutex
	trajectoryRoot string
)

// SetTrajectoryRoot names the directory the record lives in, so the fence can
// refuse it. An empty string turns the guard off, which is what every test and
// every run with recording disabled gets.
func SetTrajectoryRoot(dir string) {
	trajectoryMu.Lock()
	defer trajectoryMu.Unlock()
	trajectoryRoot = dir
}

// TrajectoryRoot reports the directory the record lives in, or "" when recording
// is off.
func TrajectoryRoot() string {
	trajectoryMu.Lock()
	defer trajectoryMu.Unlock()
	return trajectoryRoot
}

// trajectoryStoreRefusal refuses tool access to the store. It is consulted by
// CheckPathAccess, before the path fence, so a grant that happens to cover the
// directory cannot open it either.
func trajectoryStoreRefusal(ctx context.Context, path string) string {
	store := TrajectoryRoot()
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
		Reason:  "is inside the trajectory store, which holds the record of what a run did",
		Mode:    policy.Mode,
		Ask:     types.AskReserved,
		Detail:  []string{"store: " + store},
	}.Message()
}
