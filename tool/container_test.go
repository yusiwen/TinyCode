package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// stubRuntime puts a fake runtime on PATH that answers `info` with version and
// counts how often it was called.
func stubRuntime(t *testing.T, name, version string) (calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls.txt")
	script := filepath.Join(dir, name)
	body := "#!/bin/sh\necho call >> " + calls + "\necho " + version + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// withContainerConfig installs a container section for one test.
func withContainerConfig(t *testing.T, cfg *ContainerConfig, tier string) {
	t.Helper()
	previous := DefaultSandbox.Container
	previousTier := DefaultSandbox.Tier
	DefaultSandbox.Container = cfg
	DefaultSandbox.Tier = tier
	t.Cleanup(func() {
		DefaultSandbox.Container = previous
		DefaultSandbox.Tier = previousTier
	})
}

// TestProbeContainerFindsAStubRuntime: a runtime that answers `info` is available,
// and the answer is cached.
func TestProbeContainerFindsAStubRuntime(t *testing.T) {
	restore := resetContainerProbeForTest()
	t.Cleanup(restore)
	calls := stubRuntime(t, "fakedocker", "27.1.1")

	if got := ProbeContainer("fakedocker"); !got.Available || got.Version != "27.1.1" {
		t.Fatalf("probe = %+v", got)
	}
	ProbeContainer("fakedocker")
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1; lines != 1 {
		t.Fatalf("the probe ran %d times, want once", lines)
	}
}

// TestProbeContainerReportsAMissingRuntime: the reason names what is missing.
func TestProbeContainerReportsAMissingRuntime(t *testing.T) {
	restore := resetContainerProbeForTest()
	t.Cleanup(restore)

	got := ProbeContainer("no-such-runtime-anywhere")
	if got.Available {
		t.Fatalf("probe = %+v", got)
	}
	if !strings.Contains(got.Reason, "not installed") {
		t.Fatalf("reason = %q", got.Reason)
	}
}

// TestProbeContainerReportsAFailingDaemon: the client exists, the daemon does not
// answer — the case a person actually hits.
func TestProbeContainerReportsAFailingDaemon(t *testing.T) {
	restore := resetContainerProbeForTest()
	t.Cleanup(restore)
	dir := t.TempDir()
	script := filepath.Join(dir, "deadocker")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'Cannot connect to the Docker daemon' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got := ProbeContainer("deadocker")
	if got.Available || !strings.Contains(got.Reason, "Cannot connect") {
		t.Fatalf("probe = %+v", got)
	}
}

// TestResolveTier: auto falls back, a named tier does not.
func TestResolveTier(t *testing.T) {
	restore := resetContainerProbeForTest()
	t.Cleanup(restore)
	containerProbe = func(runtime string) ContainerTier {
		return ContainerTier{Runtime: runtime, Reason: "stub unavailable"}
	}
	defer resetContainerProbeForTest()()

	withContainerConfig(t, &ContainerConfig{Runtime: "fakedocker"}, "")
	if tier, _ := ResolveTier("auto"); tier != types.SandboxTierKernel {
		t.Fatalf("auto on an unavailable host = %q, want kernel", tier)
	}
	if tier, probe := ResolveTier("container"); tier != types.SandboxTierContainer || probe.Available {
		t.Fatalf("a named container tier fell back: %q %+v", tier, probe)
	}
	if tier, _ := ResolveTier(""); tier != types.SandboxTierKernel {
		t.Fatalf("the default = %q, want kernel", tier)
	}
	if tier, _ := ResolveTier("nonsense"); tier != types.SandboxTierKernel {
		t.Fatalf("a typo = %q, want kernel", tier)
	}
	if tier, _ := ResolveTier("none"); tier != types.SandboxTierNone {
		t.Fatalf("none = %q", tier)
	}
}

// TestContainerTierRefusesTheCommand: a run that requires the container tier on a
// host without one runs nothing and says why.
func TestContainerTierRefusesTheCommand(t *testing.T) {
	restore := resetContainerProbeForTest()
	t.Cleanup(restore)
	containerProbe = func(runtime string) ContainerTier {
		return ContainerTier{Runtime: runtime, Reason: "fakedocker is not installed"}
	}
	withContainerConfig(t, &ContainerConfig{Runtime: "fakedocker"}, "container")

	ctx := types.WithSandboxPolicy(context.Background(), types.SandboxPolicy{
		Mode: types.SandboxWorkspaceWrite, Tier: types.SandboxTierContainer,
	})
	_, err := bashInvocation(ctx, "echo hi")
	if err == nil || !strings.Contains(err.Error(), "requires the container tier") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("the reason is missing: %v", err)
	}
}

// TestTierStatusNamesTheTier: /sandbox reads this line.
func TestTierStatusNamesTheTier(t *testing.T) {
	restore := resetContainerProbeForTest()
	t.Cleanup(restore)
	containerProbe = func(runtime string) ContainerTier { return ContainerTier{Runtime: runtime, Reason: "stub unavailable"} }
	withContainerConfig(t, &ContainerConfig{Runtime: "fakedocker"}, "container")

	if got := TierStatus(); !strings.Contains(got, "container") || !strings.Contains(got, "UNAVAILABLE") {
		t.Fatalf("status = %q", got)
	}

	withContainerConfig(t, &ContainerConfig{Runtime: "fakedocker"}, "kernel")
	if got := TierStatus(); !strings.Contains(got, "kernel") {
		t.Fatalf("status = %q", got)
	}
}
