package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// This file is the other half of the grade (roadmap F4). The outcome comes from the
// task's own suite; the process comes from the run's trajectory, and it answers the
// questions the suite cannot: did the run read the code before it changed it, did it
// check the change with the very tests it is graded by, did it repeat a call that had
// already failed, did it scatter writes across files the task never mentioned.
//
// Nothing here is a judgement of style or a model's opinion. Each dimension is
// computed from recorded events by a rule stated below, so a reader can disagree with
// the rule and recompute the number. A dimension that needs a reader's judgement —
// whether the run was honest about what it had not verified — is deliberately absent,
// and belongs to the judge study named in the entry, not to this file.

// process is how one attempt got its answer.
type process struct {
	Steps      int `json:"steps"`
	ToolCalls  int `json:"tool_calls"`
	ToolErrors int `json:"tool_errors"`
	Refusals   int `json:"refusals"`
	Repeated   int `json:"repeated_calls"`
	Reads      int `json:"reads"`
	Writes     int `json:"writes"`
	Files      int `json:"files_written"`
	// ReadBeforeWrite is evidence gathering: at least one read of the workspace
	// happened before the first write.
	ReadBeforeWrite bool `json:"read_before_write"`
	// Tests counts invocations of the graded suite (`go test`) the run made.
	Tests int `json:"tests"`
	// Verified is the dimension that matters: a `go test` after the last write.
	// A run that tests first and writes afterwards has not checked its change.
	Verified bool `json:"verified"`
}

// readTools are the calls that gather evidence, writeTools the calls that change
// the tree. The names are the tool registry's, so a renamed tool shows up as a
// missing dimension rather than as a silent zero.
var readTools = map[string]bool{
	"read_file":      true,
	"search_files":   true,
	"lsp_definition": true,
	"lsp_references": true,
	"lsp_hover":      true,
	"lsp_symbols":    true,
}

var writeTools = map[string]bool{
	"write_file":  true,
	"edit":        true,
	"apply_patch": true,
}

// measureProcess computes the process dimensions from a run's events.
//
// It reads only what the record holds: the tool name and status always, and the
// argument text when the attempt was recorded with `capture: full` (which the
// harness asks for — a digest cannot tell `go test` from `go build`). A record
// without text still yields the counts and the read/write order; the dimensions
// that need an argument say so by staying at their zero value.
func measureProcess(events []map[string]any) process {
	var p process
	// seen is the (tool, argument digest) pairs already called: a repeat is the
	// cheapest signal that a run is stuck, because the second identical call has
	// the same result as the first.
	seen := map[string]bool{}
	lastWriteAt := -1
	lastTestAt := -1
	wroteFiles := map[string]bool{}

	for i, event := range events {
		switch event["type"] {
		case "tool.call":
			p.ToolCalls++
			tool, _ := event["tool"].(string)
			status, _ := event["status"].(string)
			if status != "" && status != "ok" {
				p.ToolErrors++
			}
			switch status {
			case "refused", "blocked", "denied":
				p.Refusals++
			}
			key := tool + "\x00" + payloadDigest(event["args"])
			if seen[key] {
				p.Repeated++
			}
			seen[key] = true

			argsText := payloadText(event["args"])
			switch {
			case readTools[tool]:
				p.Reads++
			case writeTools[tool]:
				p.Writes++
				lastWriteAt = i
				if path := writtenPath(argsText); path != "" {
					wroteFiles[path] = true
				}
			case tool == "bash":
				if runsGradedSuite(argsText) {
					p.Tests++
					lastTestAt = i
				}
			}
		case "run.end":
			p.Steps = int(number(event["steps"]))
		}
	}
	p.Files = len(wroteFiles)
	// "Before the first write" is the order of the events, which the record
	// writes in call-index order: the first write closes the evidence window.
	firstWriteAt := -1
	for i, event := range events {
		if event["type"] != "tool.call" {
			continue
		}
		tool, _ := event["tool"].(string)
		if writeTools[tool] {
			firstWriteAt = i
			break
		}
	}
	for i, event := range events {
		if firstWriteAt >= 0 && i >= firstWriteAt {
			break
		}
		if event["type"] != "tool.call" {
			continue
		}
		if tool, _ := event["tool"].(string); readTools[tool] {
			p.ReadBeforeWrite = true
			break
		}
	}
	p.Verified = lastWriteAt >= 0 && lastTestAt > lastWriteAt
	return p
}

// payloadDigest is the argument digest a repeat is keyed on.
func payloadDigest(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	digest, _ := m["sha256"].(string)
	return digest
}

// payloadText is the captured argument text, when the record has it.
func payloadText(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	text, _ := m["text"].(string)
	return text
}

// writtenPath reads the "path" argument of a write call.
func writtenPath(argsText string) string {
	if argsText == "" {
		return ""
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsText), &args); err != nil {
		return ""
	}
	return args.Path
}

// runsGradedSuite reports whether a shell command invoked the suite the tasks are
// graded by. The corpus is Go modules graded with `go test`, so that is the rule;
// a harness for another language would spell its own.
func runsGradedSuite(command string) bool {
	if command == "" {
		return false
	}
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(command), &args); err != nil {
		return false
	}
	return strings.Contains(args.Command, "go test")
}

// number reads a numeric field from an event.
func number(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}

// processSummary is one configuration's process record: medians for the counts,
// shares for the properties.
type processSummary struct {
	Configuration string
	Runs          int
	Steps         float64
	Calls         float64
	Repeated      float64
	ToolErrors    float64
	ReadFirst     float64
	Verified      float64
}

// summarizeProcess reduces the attempts of one configuration.
func summarizeProcess(config string, results []runResult) processSummary {
	summary := processSummary{Configuration: config}
	var steps, calls, repeated, errors int
	for _, r := range results {
		if r.Process == nil || r.Killed || r.Configuration != config {
			continue
		}
		summary.Runs++
		steps += r.Process.Steps
		calls += r.Process.ToolCalls
		repeated += r.Process.Repeated
		errors += r.Process.ToolErrors
		if r.Process.ReadBeforeWrite {
			summary.ReadFirst++
		}
		if r.Process.Verified {
			summary.Verified++
		}
	}
	if summary.Runs == 0 {
		return summary
	}
	summary.Steps = float64(steps) / float64(summary.Runs)
	summary.Calls = float64(calls) / float64(summary.Runs)
	if calls > 0 {
		summary.Repeated = 100 * float64(repeated) / float64(calls)
		summary.ToolErrors = 100 * float64(errors) / float64(calls)
	}
	summary.ReadFirst = 100 * summary.ReadFirst / float64(summary.Runs)
	summary.Verified = 100 * summary.Verified / float64(summary.Runs)
	return summary
}

// association is the answer to the question the roadmap entry says decides whether a
// judge is worth building: does process quality predict the outcomes the tests
// already measure? Each row splits the attempts by one property and reports the pass
// rate on either side.
type association struct {
	Property    string
	Present     int
	PresentPass float64
	Absent      int
	AbsentPass  float64
}

// associateProcess splits one configuration's attempts by each process property.
//
// It is per configuration on purpose: `plan` cannot write, so it never verifies and
// never passes, and `limited` runs out of budget before it can do either. Pooling the
// configurations would report the difference between them as if it were the effect of
// verification — the first paid run after F4 landed showed exactly that, with a
// pooled "verified → 100% pass" that was nothing but "the build configuration".
func associateProcess(results []runResult, config string) []association {
	type bucket struct {
		present, absent         int
		presentPass, absentPass int
	}
	buckets := map[string]*bucket{
		"read before writing":           {},
		"verified after the last write": {},
		"repeated a call":               {},
		"hit a tool error":              {},
	}
	for _, r := range results {
		if r.Process == nil || r.Killed || r.Configuration != config {
			continue
		}
		add := func(name string, present bool) {
			b := buckets[name]
			if present {
				b.present++
				if r.Pass {
					b.presentPass++
				}
				return
			}
			b.absent++
			if r.Pass {
				b.absentPass++
			}
		}
		add("read before writing", r.Process.ReadBeforeWrite)
		add("verified after the last write", r.Process.Verified)
		add("repeated a call", r.Process.Repeated > 0)
		add("hit a tool error", r.Process.ToolErrors > 0)
	}
	var out []association
	for name, b := range buckets {
		a := association{Property: name, Present: b.present, Absent: b.absent}
		if b.present > 0 {
			a.PresentPass = 100 * float64(b.presentPass) / float64(b.present)
		}
		if b.absent > 0 {
			a.AbsentPass = 100 * float64(b.absentPass) / float64(b.absent)
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Property < out[j].Property })
	return out
}

// renderProcess prints the process section: one row per configuration, then the
// association table. Small samples are named as such: a percentage over three
// attempts is a sentence, not a measurement.
func renderProcess(results []runResult, configurations []string) string {
	var b strings.Builder
	b.WriteString("\nThe process (roadmap F4)\n")
	b.WriteString("  What the run did, from its trajectory — not what it achieved, which is the grade above.\n\n")
	b.WriteString(fmt.Sprintf("  %-12s %5s %9s %9s %6s %8s %9s %8s\n",
		"configuration", "runs", "avg steps", "avg calls", "read%", "verify%", "repeat%", "errors%"))
	for _, name := range configurations {
		s := summarizeProcess(name, results)
		if s.Runs == 0 {
			continue
		}
		b.WriteString(fmt.Sprintf("  %-12s %5d %9.1f %9.1f %5.0f%% %7.0f%% %8.1f%% %7.1f%%\n",
			s.Configuration, s.Runs, s.Steps, s.Calls, s.ReadFirst, s.Verified, s.Repeated, s.ToolErrors))
	}

	b.WriteString("\n  Does the process predict the outcome? (within each configuration: `plan` never\n")
	b.WriteString("  verifies and `limited` runs out of budget first, so pooling them would compare\n")
	b.WriteString("  configurations rather than runs.)\n\n")
	b.WriteString(fmt.Sprintf("  %-12s %-32s %6s %7s %8s %7s\n", "configuration", "property", "with", "pass", "without", "pass"))
	for _, name := range configurations {
		rows := associateProcess(results, name)
		if len(rows) == 0 {
			continue
		}
		if summarizeProcess(name, results).Runs == 0 {
			continue
		}
		for _, a := range rows {
			b.WriteString(fmt.Sprintf("  %-12s %-32s %6d %6.0f%% %8d %6.0f%%\n",
				name, a.Property, a.Present, a.PresentPass, a.Absent, a.AbsentPass))
		}
	}
	b.WriteString("\n  A cell with three attempts is a sentence, not a measurement: read the counts, not the percentage.\n")
	return b.String()
}
