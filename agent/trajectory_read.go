package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// A reader for the record, which is what makes the format a documented artifact
// rather than an implementation detail: the list command parses the same files a
// person or a script would, so the schema's usability is asserted by a real
// consumer instead of by the writer's own tests.

// RunSummary is one run as a reader reports it.
//
// Complete is false for a file with no run.end, which is not a corrupt file: an
// interrupted run leaves every line up to the interruption, and a reader has to
// say so rather than treat the absence as an error.
type RunSummary struct {
	Path        string
	Run         string
	Parent      string
	Session     string
	Agent       string
	Provider    string
	Model       string
	Capture     string
	Stop        string
	Steps       int
	Duration    time.Duration
	Tokens      types.Usage
	Cost        types.CostTotals
	CostUnknown int
	Started     time.Time
	Lines       int
	Complete    bool
}

// TokensTotal is the number the list prints: prompt plus completion as the
// provider counted them, zero when nothing reported usage.
func (r RunSummary) TokensTotal() int { return r.Tokens.TotalTokens }

// ListRuns reads the runs in dir, newest first, and returns at most limit of
// them (limit <= 0 means all).
//
// It reads each file twice at most — the first line for the run's identity, the
// rest for the last run.end — because a trajectory is meant to be scannable
// without loading it whole.
func ListRuns(dir string, limit int) ([]RunSummary, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		names = append(names, entry.Name())
	}
	// Newest first: the names start with a UTC timestamp, so the order the store
	// already has is the order a reader wants, and pruning relies on it too.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if limit > 0 && len(names) > limit {
		names = names[:limit]
	}

	summaries := make([]RunSummary, 0, len(names))
	for _, name := range names {
		summary, err := readRun(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// readRun parses one trajectory file into a summary.
func readRun(path string) (RunSummary, error) {
	file, err := os.Open(path)
	if err != nil {
		return RunSummary{}, err
	}
	defer file.Close()

	summary := RunSummary{Path: path, Run: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
	scanner := bufio.NewScanner(file)
	// A captured payload can be 64 KiB on one line, and the scanner's default
	// buffer is smaller than that.
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		summary.Lines++
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			// One unreadable line does not invalidate the run: the file is
			// appended to by a process that can be killed, and a half-written
			// last line is the normal shape of an interruption.
			continue
		}
		switch event["type"] {
		case TrajectoryRunStart:
			summary.Complete = false // a later run.end may set it
			summary.Parent = stringField(event, "parent")
			summary.Session = stringField(event, "session")
			summary.Agent = stringField(event, "agent")
			summary.Provider = stringField(event, "provider")
			summary.Model = stringField(event, "model")
			summary.Capture = stringField(event, "capture")
			summary.Started = timeField(event, "at")
		case TrajectoryRunEnd:
			summary.Complete = true
			summary.Stop = stringField(event, "stop")
			summary.Steps = int(numberField(event, "steps"))
			summary.Duration = time.Duration(numberField(event, "duration_ms")) * time.Millisecond
			summary.CostUnknown = int(numberField(event, "cost_unknown_calls"))
			summary.Tokens = tokensField(event)
			summary.Cost = costField(event)
		}
	}
	if err := scanner.Err(); err != nil {
		return RunSummary{}, err
	}
	return summary, nil
}

func stringField(event map[string]any, field string) string {
	value, _ := event[field].(string)
	return value
}

func numberField(event map[string]any, field string) float64 {
	value, _ := event[field].(float64)
	return value
}

func timeField(event map[string]any, field string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, stringField(event, field))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// tokensField reads the usage object back. The keys are types.Usage's own JSON
// names, so the reader and the writer cannot drift.
func tokensField(event map[string]any) types.Usage {
	object, ok := event["tokens"].(map[string]any)
	if !ok {
		return types.Usage{}
	}
	return types.Usage{
		PromptTokens:       int(numberField(object, "prompt_tokens")),
		CompletionTokens:   int(numberField(object, "completion_tokens")),
		TotalTokens:        int(numberField(object, "total_tokens")),
		CachedPromptTokens: int(numberField(object, "cached_prompt_tokens")),
		CacheWriteTokens:   int(numberField(object, "cache_write_tokens")),
		ReasoningTokens:    int(numberField(object, "reasoning_tokens")),
	}
}

// costField reads the cost array back. It is an array rather than an object so a
// file's order is fixed; the reader does not care, but it must accept both the
// array's units and an amount of zero.
func costField(event map[string]any) types.CostTotals {
	entries, ok := event["cost"].([]any)
	if !ok {
		return nil
	}
	totals := make(types.CostTotals, len(entries))
	for _, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		totals[stringField(object, "unit")] += numberField(object, "amount")
	}
	return totals
}

// ValidRunID reports whether a run id is one this store could have written.
//
// The check is the path-traversal guard: the id becomes a file name, and an id
// like "../../etc/passwd" would read a file the store never wrote.
func ValidRunID(id string) bool {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return false
	}
	if strings.ContainsAny(id, " \t\n") {
		return false
	}
	return true
}
