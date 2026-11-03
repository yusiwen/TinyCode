package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FileChange is one entry of the working tree's state, labelled with whether a
// tool call of the run being reported wrote it.
type FileChange struct {
	// Path is relative to the repository root.
	Path string
	// Status is the porcelain code: "M", "A", "D", "??" and so on.
	Status string
	// Added and Removed are line counts against HEAD; an untracked file counts
	// its lines as added and nothing as removed.
	Added   int
	Removed int
	// ByRun is true when a checkpoint of this run's tool calls names the path.
	ByRun bool
}

// ChangeSet is what RunChanges found: the whole working tree, split by
// attribution.
type ChangeSet struct {
	Files []FileChange
	// Since is the window the attribution used; zero means "the whole session".
	Since time.Time
}

// Run reports the files a tool call of this run wrote.
func (c ChangeSet) Run() []FileChange {
	return c.filter(true)
}

// Other reports the changes no tool call of this run accounted for — including
// anything a shell command wrote, which the checkpoint cannot see.
func (c ChangeSet) Other() []FileChange {
	return c.filter(false)
}

func (c ChangeSet) filter(byRun bool) []FileChange {
	var out []FileChange
	for _, file := range c.Files {
		if file.ByRun == byRun {
			out = append(out, file)
		}
	}
	return out
}

// Empty reports whether the working tree is clean.
func (c ChangeSet) Empty() bool { return len(c.Files) == 0 }

// Totals sums the line counts over every file.
func (c ChangeSet) Totals() (added, removed int) {
	for _, file := range c.Files {
		added += file.Added
		removed += file.Removed
	}
	return added, removed
}

// Report renders the change set for the transcript: one line per file, marked
// with who wrote it, and a note about what the checkpoint cannot see.
func (c ChangeSet) Report() string {
	if c.Empty() {
		return "No changes in the working tree."
	}
	added, removed := c.Totals()
	var b strings.Builder
	fmt.Fprintf(&b, "%d file(s) changed, +%d/-%d — %d written by this run's tool calls, %d other.\n",
		len(c.Files), added, removed, len(c.Run()), len(c.Other()))
	width := 0
	for _, file := range c.Files {
		if len(file.Path) > width {
			width = len(file.Path)
		}
	}
	if width > 60 {
		width = 60
	}
	for _, file := range c.Files {
		label := "other"
		if file.ByRun {
			label = "this run"
		}
		fmt.Fprintf(&b, "  %-2s  %-*s  +%d/-%d  %s\n", file.Status, width, file.Path, file.Added, file.Removed, label)
	}
	b.WriteString("A shell command can write files the checkpoint does not see; `git status` is the full truth.")
	return b.String()
}

// RunChanges reports the working tree with each change attributed to the run's
// tool calls when a checkpoint names it.
//
// The working tree is the full set on purpose: checkpoints only cover writes
// that went through a tool, while bash can write anything. since bounds the
// attribution by time rather than by run number, because the run counter is
// per process and a resumed session restarts it (see types.ToolCallScope).
func RunChanges(ctx context.Context, session string, since time.Time) (ChangeSet, error) {
	root, err := gitOutput(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return ChangeSet{}, fmt.Errorf("not inside a git repository: %w", err)
	}
	status, err := gitOutput(ctx, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return ChangeSet{}, err
	}
	numstat, _ := gitOutput(ctx, "diff", "--numstat", "HEAD")

	counts := map[string][2]int{}
	for _, line := range strings.Split(numstat, "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		added, errA := strconv.Atoi(fields[0])
		removed, errR := strconv.Atoi(fields[1])
		if errA != nil || errR != nil {
			continue // a binary file reports "-"
		}
		counts[fields[2]] = [2]int{added, removed}
	}

	var files []FileChange
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		statusCode, path, ok := parsePorcelain(line)
		if !ok {
			continue
		}
		change := FileChange{Path: path, Status: statusCode}
		if pair, ok := counts[path]; ok {
			change.Added, change.Removed = pair[0], pair[1]
		} else if strings.Contains(statusCode, "?") {
			change.Added = countLines(filepath.Join(root, path))
		}
		files = append(files, change)
	}

	attributed, err := runAttribution(root, session, since)
	if err != nil {
		return ChangeSet{}, err
	}
	for i := range files {
		files[i].ByRun = attributed[files[i].Path]
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return ChangeSet{Files: files, Since: since}, nil
}

// parsePorcelain reads one `git status --porcelain=v1` line: two status columns,
// a space, then the path. A rename carries "old -> new", and the new name is the
// one that exists now.
func parsePorcelain(line string) (status, path string, ok bool) {
	if len(line) < 4 {
		return "", "", false
	}
	status = strings.TrimSpace(line[:2])
	path = strings.TrimSpace(line[3:])
	if i := strings.LastIndex(path, " -> "); i >= 0 {
		path = path[i+4:]
	}
	path = strings.Trim(path, `"`)
	if path == "" {
		return "", "", false
	}
	return status, path, true
}

// runAttribution reports which repository-relative paths the session's tool calls
// wrote at or after since. A call with no session recorded is treated as this
// session's, matching LastCall: the TUI mints its id after the first call it
// records.
func runAttribution(root, session string, since time.Time) (map[string]bool, error) {
	calls, err := ListCalls(0)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]bool)
	for _, call := range calls {
		if call.Restored() {
			continue // an undone write left nothing of its own in the tree
		}
		if session != "" && call.Session != "" && call.Session != session {
			continue
		}
		if !since.IsZero() && call.Time.Before(since) {
			continue
		}
		for _, path := range call.Paths() {
			// A checkpoint stores the path the write had: absolute in
			// production, already repository-relative in a test that recorded
			// one that way.
			rel := path
			if filepath.IsAbs(path) {
				if r, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(r, "..") {
					rel = r
				}
			}
			paths[rel] = true
		}
	}
	return paths, nil
}

// gitOutput runs git in the current directory and returns its trimmed stdout.
// It asks git not to quote unusual paths, so the parser above can stay simple.
func gitOutput(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"-c", "core.quotepath=false"}, args...)
	return gitExec(ctx, full...)
}

// countLines counts an untracked file's lines, reading at most 4 MiB: the number
// is a size hint for a person, not an audit.
func countLines(path string) int {
	const limit = 4 << 20
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, _ := f.Read(buf)
	lines := strings.Count(string(buf[:n]), "\n")
	if n > 0 && buf[n-1] != '\n' {
		lines++
	}
	return lines
}
