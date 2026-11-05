package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/agent"
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

// TestContainerInvocationIsBuiltFromConfig: every flag comes from the policy or the
// configuration, and none from the model.
func TestContainerInvocationIsBuiltFromConfig(t *testing.T) {
	cfg := &ContainerConfig{Runtime: "podman", Image: "golang:1.27", Network: "bridge", Memory: "2g", CPUs: "2", PIDs: 512}
	policy := types.SandboxPolicy{Mode: types.SandboxWorkspaceWrite, ProjectRoot: "/tmp/project"}

	argv, err := ContainerInvocation(policy, cfg, "go test ./...")
	if err != nil {
		t.Fatalf("ContainerInvocation: %v", err)
	}
	line := strings.Join(argv, " ")
	for _, want := range []string{
		"podman run --rm --init --pull never --network bridge",
		"--workdir /work",
		"-v /tmp/project:/work",
		"--tmpfs /tmp:rw,exec",
		"-e HOME=/tmp/home",
		"--memory 2g", "--cpus 2", "--pids-limit 512",
		"golang:1.27 bash -c go test ./...",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("argv is missing %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "/work:ro") {
		t.Fatalf("a writable policy mounted read-only:\n%s", line)
	}
	if !strings.Contains(line, fmt.Sprintf("--user %d:%d", os.Getuid(), os.Getgid())) {
		t.Errorf("the container does not run as the caller:\n%s", line)
	}
}

// TestContainerInvocationMountsReadOnlyForAReadOnlyRun: plan mode's read-only
// policy must not become writable because it moved into a container.
func TestContainerInvocationMountsReadOnlyForAReadOnlyRun(t *testing.T) {
	argv, err := ContainerInvocation(
		types.SandboxPolicy{Mode: types.SandboxReadOnly, ProjectRoot: "/tmp/project"},
		&ContainerConfig{Image: "img"}, "echo hi")
	if err != nil {
		t.Fatalf("ContainerInvocation: %v", err)
	}
	if !strings.Contains(strings.Join(argv, " "), "/tmp/project:/work:ro") {
		t.Fatalf("argv = %v", argv)
	}
	// The default network is none: a command does not reach the network unless
	// the configuration says so.
	if !strings.Contains(strings.Join(argv, " "), "--network none") {
		t.Fatalf("argv = %v", argv)
	}
}

// TestContainerInvocationNeedsAnImage: no image, no container — and the message
// names the key to set.
func TestContainerInvocationNeedsAnImage(t *testing.T) {
	_, err := ContainerInvocation(types.SandboxPolicy{ProjectRoot: "/tmp/p"}, &ContainerConfig{}, "true")
	if err == nil || !strings.Contains(err.Error(), "sandbox.container.image") {
		t.Fatalf("err = %v", err)
	}
}

// TestBashInvocationUsesTheContainer: the spawn replaces the launcher when the
// tier is the container and the host has a runtime.
func TestBashInvocationUsesTheContainer(t *testing.T) {
	restore := resetContainerProbeForTest()
	t.Cleanup(restore)
	stubRuntime(t, "fakedocker", "27.1.1")
	withContainerConfig(t, &ContainerConfig{Runtime: "fakedocker", Image: "img"}, "container")

	ctx := types.WithSandboxPolicy(context.Background(), types.SandboxPolicy{
		Mode: types.SandboxWorkspaceWrite, Tier: types.SandboxTierContainer, ProjectRoot: "/tmp/project",
	})
	argv, err := bashInvocation(ctx, "echo hi")
	if err != nil {
		t.Fatalf("bashInvocation: %v", err)
	}
	if argv[0] != "fakedocker" || argv[1] != "run" {
		t.Fatalf("argv = %v", argv)
	}
	if !strings.Contains(strings.Join(argv, " "), "img bash -c echo hi") {
		t.Fatalf("argv = %v", argv)
	}
}

// TestContainerSpawnRunsWhenTheHostHasARuntime is the gated end-to-end: it needs a
// real runtime and an image, so it runs only with CONTAINER_TEST=1.
func TestContainerSpawnRunsWhenTheHostHasARuntime(t *testing.T) {
	if os.Getenv("CONTAINER_TEST") != "1" {
		t.Skip("set CONTAINER_TEST=1 with a container runtime and image to run this")
	}
	restore := resetContainerProbeForTest()
	t.Cleanup(restore)
	probe := ProbeContainer(containerRuntime())
	if !probe.Available {
		t.Skipf("no container runtime: %s", probe.Reason)
	}
	image := os.Getenv("CONTAINER_TEST_IMAGE")
	if image == "" {
		t.Skip("set CONTAINER_TEST_IMAGE to an image with bash and coreutils")
	}
	withContainerConfig(t, &ContainerConfig{Image: image}, "container")
	project := t.TempDir()

	ctx := types.WithSandboxPolicy(context.Background(), types.SandboxPolicy{
		Mode: types.SandboxWorkspaceWrite, Tier: types.SandboxTierContainer, ProjectRoot: project,
	})
	out, err := Bash().Execute(ctx, map[string]any{"command": "pwd && echo written > file.txt && cat file.txt && (curl -sS -m 2 https://example.com >/dev/null && echo NETWORK-OPEN || echo NETWORK-CLOSED)"})
	if err != nil {
		t.Fatalf("bash in the container: %v", err)
	}
	if !strings.Contains(out, "/work") || !strings.Contains(out, "written") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(out, "NETWORK-CLOSED") {
		t.Fatalf("the network is not closed: %q", out)
	}
	if _, err := os.Stat(project + "/file.txt"); err != nil {
		t.Fatalf("the write did not reach the project mount: %v", err)
	}
}

// TestReadOnlyTierNarrowsWhenTheHostCan: an explorer's commands lose the network
// and the writable project, and nothing it could do is granted.
func TestReadOnlyTierNarrowsWhenTheHostCan(t *testing.T) {
	restore := resetContainerProbeForTest()
	t.Cleanup(restore)
	stubRuntime(t, "fakedocker", "27.1.1")
	withContainerConfig(t, &ContainerConfig{Runtime: "fakedocker", Image: "img"}, "kernel")

	policy := types.SandboxPolicy{Mode: types.SandboxWorkspaceWrite, Tier: types.SandboxTierKernel, ProjectRoot: "/tmp/p"}
	narrowed, ok := ReadOnlyTier(policy)
	if !ok {
		t.Fatal("a read-only agent was not narrowed")
	}
	if narrowed.Tier != types.SandboxTierContainer {
		t.Fatalf("tier = %q", narrowed.Tier)
	}
	if narrowed.Mode != types.SandboxReadOnly {
		t.Fatalf("mode = %q, want read-only so the project mount is :ro", narrowed.Mode)
	}
	if policy.Tier != types.SandboxTierKernel {
		t.Fatal("the caller's policy was modified")
	}
}

// TestReadOnlyTierKeepsTheSessionTierWhenItCannot: a host without a runtime must
// not fail a task that could have run.
func TestReadOnlyTierKeepsTheSessionTierWhenItCannot(t *testing.T) {
	restore := resetContainerProbeForTest()
	t.Cleanup(restore)
	containerProbe = func(runtime string) ContainerTier {
		return ContainerTier{Runtime: runtime, Reason: "stub unavailable"}
	}
	withContainerConfig(t, &ContainerConfig{Runtime: "fakedocker", Image: "img"}, "kernel")

	policy := types.SandboxPolicy{Mode: types.SandboxWorkspaceWrite, Tier: types.SandboxTierKernel, ProjectRoot: "/tmp/p"}
	got, ok := ReadOnlyTier(policy)
	if ok || got.Tier != types.SandboxTierKernel || got.Mode != types.SandboxWorkspaceWrite {
		t.Fatalf("narrowed without a runtime: ok=%v policy=%+v", ok, got)
	}
}

// TestReadOnlyTierDoesNothingWithoutAnImage: a runtime with no image is not a
// container tier.
func TestReadOnlyTierDoesNothingWithoutAnImage(t *testing.T) {
	restore := resetContainerProbeForTest()
	t.Cleanup(restore)
	stubRuntime(t, "fakedocker", "27.1.1")
	withContainerConfig(t, &ContainerConfig{Runtime: "fakedocker"}, "kernel")

	if _, ok := ReadOnlyTier(types.SandboxPolicy{Tier: types.SandboxTierKernel}); ok {
		t.Fatal("narrowed without an image")
	}
}

// TestReadOnlySubagentRunsInTheContainer is the integration half: a read-only
// sub-agent's `bash` reaches the runtime, which records the argv.
func TestReadOnlySubagentRunsInTheContainer(t *testing.T) {
	restore := resetContainerProbeForTest()
	t.Cleanup(restore)
	argsFile := filepath.Join(t.TempDir(), "argv.txt")
	dir := t.TempDir()
	script := filepath.Join(dir, "fakedocker")
	body := "#!/bin/sh\necho \"$@\" > " + argsFile + "\necho ran-in-container\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	withContainerConfig(t, &ContainerConfig{Runtime: "fakedocker", Image: "img"}, "kernel")
	// A container tier mounts the project; without a root there is nothing to run in.
	project, _ := checkpointTestSetup(t)
	t.Cleanup(func() { DefaultSandbox.ProjectRoot = project })

	// The sub-agent calls bash; the fake runtime records what it was asked to run.
	provider := &agent.MockProvider{ChatFunc: func(_ context.Context, _ types.ChatRequest) (*types.ChatResponse, error) {
		return &types.ChatResponse{ToolCalls: []types.ToolCall{{
			ID: "c1", Name: "bash", Arguments: `{"command":"echo hi"}`,
		}}}, nil
	}}
	bashTool := Bash()
	deps := &TaskToolDeps{
		AllTools: []agent.Tool{{
			Name: bashTool.Name, Description: bashTool.Description,
			Parameters: bashTool.Parameters, Execute: bashTool.Execute,
		}},
		GetAgentConfig: func(name string) *agent.AgentConfig {
			return &agent.AgentConfig{Name: name, AllowedTools: []string{"bash"}, MaxSteps: 3}
		},
		ResolveProvider: func(string) (agent.LLMProvider, error) { return provider, nil },
	}
	if _, err := TaskTool(deps).Execute(taskCtx(), map[string]any{"agent": "explore", "goal": "look around"}); err != nil {
		t.Fatalf("task: %v", err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("the container was not used: %v", err)
	}
	line := string(args)
	for _, want := range []string{"run --rm", "--network none", ":/work:ro", "img bash -c echo hi"} {
		if !strings.Contains(line, want) {
			t.Errorf("argv is missing %q:\n%s", want, line)
		}
	}
}

// TestReadOnlyTierNeedsAProjectRoot: a container has nothing to mount without one,
// so narrowing would only break the task.
func TestReadOnlyTierNeedsAProjectRoot(t *testing.T) {
	restore := resetContainerProbeForTest()
	t.Cleanup(restore)
	stubRuntime(t, "fakedocker", "27.1.1")
	withContainerConfig(t, &ContainerConfig{Runtime: "fakedocker", Image: "img"}, "kernel")

	if _, ok := ReadOnlyTier(types.SandboxPolicy{Tier: types.SandboxTierKernel}); ok {
		t.Fatal("narrowed without a project root")
	}
}
