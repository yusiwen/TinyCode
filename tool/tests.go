package tool

import (
	"context"
	"time"

	"github.com/yusiwen/tinycode/verify"
)

// RunTests is the model-facing way to ask for the project's suite (roadmap C5).
//
// The command is the project's own — discovered from its marker files or set in
// `test.command` — so the tool runs it with the user's permissions in the project
// root. What it bounds is the cost: a timeout, and a tail of the output instead of
// the whole transcript of a failing suite.
func RunTests(settings verify.Settings) Tool {
	return Tool{
		Name: "run_tests",
		Description: "Run the project's test suite and return a bounded summary: pass/fail, the exit code, and the last part of the output. " +
			"The command is discovered (go.mod, package.json's test script, a Makefile test target) or set as test.command; " +
			"use it after changing code to see whether the project still passes.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Execute: func(ctx context.Context, _ map[string]any) (string, error) {
			command, err := verify.Discover(settings.Dir, settings.Config)
			if err != nil {
				return "", err
			}
			report, err := verify.Run(ctx, settings.Dir, command, verify.Options{
				Timeout:   timeoutOf(settings),
				MaxOutput: settings.Config.MaxOutputBytes,
			})
			if err != nil {
				return "", err
			}
			return report.String(), nil
		},
	}
}

// timeoutOf turns the configured milliseconds into a duration; zero lets verify
// apply its default.
func timeoutOf(settings verify.Settings) time.Duration {
	if settings.Config.TimeoutMS <= 0 {
		return 0
	}
	return time.Duration(settings.Config.TimeoutMS) * time.Millisecond
}
