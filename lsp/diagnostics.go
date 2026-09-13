package lsp

import (
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// maxDetailMessages caps how many diagnostic messages are folded into one
// DiagnosticsInfo.Details line, so a file with many errors still yields a
// single bounded line.
const maxDetailMessages = 3

// DiagnosticsInfo is an immutable, point-in-time copy of the LSP diagnostics
// registry. It is what the TUI renders in its status bar and in /diagnostics.
type DiagnosticsInfo struct {
	// Files is the number of files with at least one ERROR-level diagnostic.
	Files int
	// Errors is the total number of ERROR-level diagnostics across all files.
	Errors int
	// LastPath is the file with errors whose diagnostics were updated most
	// recently. It is empty when no file has errors.
	LastPath string
	// Details holds one human-readable line per affected file, ordered by path.
	Details []string
}

var (
	// diagMu guards diagRegistry and diagLastPath. It is deliberately separate
	// from mu (server lifecycle state) so a registry read never contends with
	// starting or inspecting the LSP server.
	diagMu sync.Mutex
	// diagRegistry holds the latest ERROR-level diagnostics per absolute file
	// path. An absent entry means "no known errors" for that file.
	diagRegistry map[string][]Diagnostic
	// diagLastPath tracks the file whose diagnostics were recorded most
	// recently and still have errors.
	diagLastPath string
)

// DiagnosticsSnapshot returns a copy of the current per-file diagnostics state.
// It only reads in-memory state and never talks to the LSP server, so it is
// safe to call from any goroutine (including a Bubble Tea command).
func DiagnosticsSnapshot() DiagnosticsInfo {
	diagMu.Lock()
	defer diagMu.Unlock()

	paths := make([]string, 0, len(diagRegistry))
	for p := range diagRegistry {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	info := DiagnosticsInfo{
		Files:    len(paths),
		LastPath: diagLastPath,
	}
	if len(paths) > 0 {
		info.Details = make([]string, 0, len(paths))
	}
	for _, p := range paths {
		diags := diagRegistry[p]
		info.Errors += len(diags)
		info.Details = append(info.Details, formatDetailLine(p, diags))
	}
	return info
}

// DiagnosticsSummary returns the number of files with errors and the total
// number of errors across all files.
func DiagnosticsSummary() (files int, errors int) {
	info := DiagnosticsSnapshot()
	return info.Files, info.Errors
}

// DiagnosticsDetails returns one line per file with errors, each carrying the
// file's error count and (up to maxDetailMessages) its messages.
func DiagnosticsDetails() []string {
	return DiagnosticsSnapshot().Details
}

// recordDiagnostics stores the latest diagnostics for path and drops the file
// when it has no ERROR-level diagnostic, so a fixed file disappears from the
// summary. It is safe for concurrent use and never performs LSP I/O.
func recordDiagnostics(path string, diags []Diagnostic) {
	if path == "" {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil || abs == "" {
		abs = path
	}

	errorsOnly := make([]Diagnostic, 0, len(diags))
	for _, d := range diags {
		if d.Severity == 1 {
			errorsOnly = append(errorsOnly, d)
		}
	}

	diagMu.Lock()
	defer diagMu.Unlock()
	if diagRegistry == nil {
		diagRegistry = make(map[string][]Diagnostic)
	}

	if len(errorsOnly) == 0 {
		delete(diagRegistry, abs)
		if diagLastPath == abs {
			diagLastPath = anyPathLocked()
		}
		return
	}
	diagRegistry[abs] = errorsOnly
	diagLastPath = abs
}

// anyPathLocked returns a deterministic path still present in the registry, or
// "" when it is empty. The caller must hold diagMu.
func anyPathLocked() string {
	first := ""
	for p := range diagRegistry {
		if first == "" || p < first {
			first = p
		}
	}
	return first
}

// resetDiagnostics clears the registry. It is called by Init so stale
// diagnostics from a previous workspace never leak into a new session.
func resetDiagnostics() {
	diagMu.Lock()
	diagRegistry = nil
	diagLastPath = ""
	diagMu.Unlock()
}

// formatDetailLine renders one summary line for a file's error diagnostics.
func formatDetailLine(path string, diags []Diagnostic) string {
	msgs := make([]string, 0, len(diags))
	for i, d := range diags {
		if i >= maxDetailMessages {
			msgs = append(msgs, fmt.Sprintf("(+%d more)", len(diags)-maxDetailMessages))
			break
		}
		// LSP positions are 0-based; display them 1-based like the formatter.
		msgs = append(msgs, fmt.Sprintf("%d:%d %s",
			d.Range.Start.Line+1, d.Range.Start.Character+1, d.Message))
	}
	return fmt.Sprintf("%s: %d error(s) - %s", path, len(diags), strings.Join(msgs, "; "))
}

// uriToPath converts a file:// document URI into a filesystem path. Non-file
// URIs are returned unchanged so callers can still key the registry on them.
func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return uri
	}
	if u.Path == "" {
		return uri
	}
	return u.Path
}
