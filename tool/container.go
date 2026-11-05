package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// The container tier (roadmap C6) is the first one that can bound reads and the
// network as well as writes: a command runs inside an image instead of in the
// session's shell. This file owns the two questions a host has to answer before a
// run may use it — is there a runtime, and what is its version — and the report a
// person reads when the answer is no.

// ContainerConfig is the container tier's runtime and limits, copied from the
// configuration so a run's policy can be built without this package importing it.
type ContainerConfig struct {
	Runtime string
	Image   string
	Network string
	Memory  string
	CPUs    string
	PIDs    int
}

// probeTimeout bounds the runtime probe. A missing daemon must not cost more than
// a command's startup.
const probeTimeout = 6 * time.Second

// ContainerTier is what this host can do with a container runtime.
type ContainerTier struct {
	Runtime   string
	Available bool
	Version   string
	// Reason says why it is unavailable, in the words a person can act on.
	Reason string
}

func (c ContainerTier) String() string {
	if !c.Available {
		return fmt.Sprintf("unavailable (%s)", c.Reason)
	}
	if c.Version == "" {
		return fmt.Sprintf("%s (available)", c.Runtime)
	}
	return fmt.Sprintf("%s %s (available)", c.Runtime, c.Version)
}

// containerProbe is the capability question behind a variable so a test can
// present a host with or without a runtime; production never reassigns it.
var containerProbe = probeContainer

// containerCache remembers the probe per runtime: the answer cannot change
// within a process in any way worth re-asking on every command.
var containerCache struct {
	mu        sync.Mutex
	byRuntime map[string]ContainerTier
}

// ProbeContainer reports whether runtime can run a container on this host.
func ProbeContainer(runtime string) ContainerTier {
	if runtime == "" {
		runtime = "docker"
	}
	containerCache.mu.Lock()
	defer containerCache.mu.Unlock()
	if containerCache.byRuntime == nil {
		containerCache.byRuntime = map[string]ContainerTier{}
	}
	if cached, ok := containerCache.byRuntime[runtime]; ok {
		return cached
	}
	result := containerProbe(runtime)
	containerCache.byRuntime[runtime] = result
	return result
}

// resetContainerProbeForTest clears the cache and swaps the probe, returning the
// restore function.
func resetContainerProbeForTest() func() {
	previous := containerProbe
	containerCache.mu.Lock()
	previousCache := containerCache.byRuntime
	containerCache.byRuntime = nil
	containerCache.mu.Unlock()
	return func() {
		containerProbe = previous
		containerCache.mu.Lock()
		containerCache.byRuntime = previousCache
		containerCache.mu.Unlock()
	}
}

// probeContainer asks the runtime for its server version. `info` talks to the
// daemon (unlike `version`, which also answers without one), which is the fact
// that matters: a command would need the daemon, not the client.
func probeContainer(runtime string) ContainerTier {
	result := ContainerTier{Runtime: runtime}
	path, err := exec.LookPath(runtime)
	if err != nil {
		result.Reason = fmt.Sprintf("%s is not installed", runtime)
		return result
	}
	cmd := exec.Command(path, "info", "--format", "{{.ServerVersion}}")
	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() {
		out, runErr = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(probeTimeout):
		_ = cmd.Process.Kill()
		<-done
		result.Reason = fmt.Sprintf("%s did not answer within %s (is the daemon running?)", runtime, probeTimeout)
		return result
	}
	if runErr != nil {
		text := strings.TrimSpace(string(out))
		if text == "" {
			text = runErr.Error()
		}
		result.Reason = fmt.Sprintf("%s info failed: %s", runtime, text)
		return result
	}
	result.Available = true
	result.Version = strings.TrimSpace(string(out))
	return result
}

// containerRuntime is the runtime a container tier would use: the configured one,
// docker by default.
func containerRuntime() string {
	if DefaultSandbox.Container != nil && DefaultSandbox.Container.Runtime != "" {
		return DefaultSandbox.Container.Runtime
	}
	return "docker"
}

// ResolveTier turns the configured tier into the tier a run will use, and the
// probe result that justifies it.
//
// `auto` is the only value that falls back: it picks the container when the host
// has one and the kernel tier otherwise. A named tier is a promise, so an
// unavailable container stays selected and the command is refused — the same
// fail-closed rule `require_hard_boundary` follows.
func ResolveTier(requested string) (types.SandboxTier, ContainerTier) {
	probe := ProbeContainer(containerRuntime())
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "", "kernel":
		return types.SandboxTierKernel, probe
	case "none", "danger-full-access":
		return types.SandboxTierNone, probe
	case "container":
		return types.SandboxTierContainer, probe
	case "auto":
		if probe.Available {
			return types.SandboxTierContainer, probe
		}
		return types.SandboxTierKernel, probe
	default:
		// An unknown value is the kernel tier, not the widest one: a typo must
		// not move a run outside its boundary.
		return types.SandboxTierKernel, probe
	}
}

// TierStatus describes the tier the next run would use, for /sandbox.
func TierStatus() string {
	tier, probe := ResolveTier(DefaultSandbox.Tier)
	switch tier {
	case types.SandboxTierContainer:
		if !probe.Available {
			return fmt.Sprintf("container — UNAVAILABLE on this host: %s; commands are refused", probe.Reason)
		}
		return fmt.Sprintf("container (%s)", probe)
	case types.SandboxTierNone:
		return "none (commands run with no boundary)"
	default:
		return fmt.Sprintf("kernel (%s tier %s)", containerRuntime(), probe)
	}
}

// containerUnavailableError is the refusal a command gets when the run requires
// the container tier and this host cannot provide it.
func containerUnavailableError(probe ContainerTier) error {
	return fmt.Errorf("this run requires the container tier, but %s: refusing rather than running the command in a weaker tier (roadmap C6)", probe.Reason)
}

// containerImage is the image a container tier runs in, from the configuration.
func containerImage() string {
	if DefaultSandbox.Container != nil {
		return DefaultSandbox.Container.Image
	}
	return ""
}

// ContainerInvocation builds the argv that runs one shell command inside the
// image (roadmap C6, S2).
//
// Every argument comes from the configuration or the run's policy; none comes
// from the model. The shape is deliberately the smallest one that is useful: the
// project mounted where the command expects it, a writable /tmp for scratch and
// the toolchain caches, no environment pass-through, no docker socket, and no
// implicit pull — a missing image is an error a person fixes, not a download this
// function starts.
func ContainerInvocation(policy types.SandboxPolicy, cfg *ContainerConfig, cmdStr string) ([]string, error) {
	if cfg == nil || strings.TrimSpace(cfg.Image) == "" {
		return nil, fmt.Errorf("the container tier needs an image: set sandbox.container.image (the container's security is the image's)")
	}
	root := policy.ProjectRoot
	if root == "" {
		return nil, fmt.Errorf("the container tier needs a project root to mount; none is configured")
	}
	runtime := cfg.Runtime
	if runtime == "" {
		runtime = "docker"
	}
	network := cfg.Network
	if network == "" {
		network = "none"
	}

	target := "/work"
	argv := []string{runtime, "run", "--rm", "--init", "--pull", "never", "--network", network}
	// The same uid/gid as the caller, so a file the command writes is owned by the
	// person who asked for it rather than by root.
	argv = append(argv, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	argv = append(argv, "--workdir", target)
	mount := root + ":" + target
	if policy.Mode == types.SandboxReadOnly {
		mount += ":ro"
	}
	argv = append(argv, "-v", mount)
	// Scratch and the toolchain caches live in a tmpfs: writable, disposable, and
	// unable to reach the host's cache. The cost is a cold cache per command,
	// which is honest for a tier whose point is not to touch the host.
	argv = append(argv, "--tmpfs", "/tmp:rw,exec")
	argv = append(argv,
		"-e", "HOME=/tmp/home",
		"-e", "TMPDIR=/tmp",
		"-e", "GOCACHE=/tmp/gocache",
		"-e", "GOMODCACHE=/tmp/gomodcache",
		"-e", "GOPATH=/tmp/gopath",
	)
	if cfg.Memory != "" {
		argv = append(argv, "--memory", cfg.Memory)
	}
	if cfg.CPUs != "" {
		argv = append(argv, "--cpus", cfg.CPUs)
	}
	if cfg.PIDs > 0 {
		argv = append(argv, "--pids-limit", fmt.Sprintf("%d", cfg.PIDs))
	}
	argv = append(argv, cfg.Image, "bash", "-c", cmdStr)
	return argv, nil
}

// containerInvocationFor is bashInvocation's decision, split out so the tier's
// errors and the fallbacks live in one place.
func containerInvocationFor(ctx context.Context, policy types.SandboxPolicy, cmdStr string) ([]string, error) {
	probe := ProbeContainer(containerRuntime())
	if !probe.Available {
		return nil, containerUnavailableError(probe)
	}
	return ContainerInvocation(policy, DefaultSandbox.Container, cmdStr)
}
