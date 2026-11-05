package tool

import (
	"fmt"
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
