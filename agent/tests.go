package agent

import (
	"context"
	"time"

	"github.com/yusiwen/tinycode/verify"
)

// runTestsForResult appends the project's suite to a write tool's result when
// `test.auto_after_write` is on (roadmap C5).
//
// It is deliberately part of the same tool result: the model reads the write and
// its verification together, and the loop's repeat guard still sees one call. A
// project without a test command is reported in the same place rather than
// failing the write — the write did happen.
func (a *Agent) runTestsForResult(ctx context.Context, result string) string {
	if a.Tests == nil {
		return result
	}
	command, err := verify.Discover(a.Tests.Dir, a.Tests.Config)
	if err != nil {
		return result + "\n[tests] " + err.Error()
	}
	report, err := verify.Run(ctx, a.Tests.Dir, command, verify.Options{
		Timeout:   testTimeout(a.Tests.Config.TimeoutMS),
		MaxOutput: a.Tests.Config.MaxOutputBytes,
	})
	if err != nil {
		return result + "\n[tests] " + err.Error()
	}
	return result + "\n[tests] " + report.String()
}

// testTimeout turns the configured milliseconds into the runner's bound; zero
// lets verify apply its default.
func testTimeout(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}
