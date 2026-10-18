package agent

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// A trajectory is what a run leaves behind for someone who wants to measure it
// later: one JSONL file per run, one event per line, each event carrying its own
// duration. It is the raw material an evaluation harness needs (roadmap F1) and
// the reason `tlog` is not it — a log line is for a human reading a failure, a
// trajectory line is for a program comparing two runs.
//
// Three properties are deliberate and everything below follows from them.
//
// It never fails a run. A trajectory is observability, not safety: the
// checkpoint store refuses a write it cannot make undoable, while this refuses
// only to record. A full disk therefore produces a warning and a run that still
// answers, which is the opposite of the checkpoint rule on purpose — the two are
// not inconsistent, they are answering different questions.
//
// It is append-only and flushed per event. A run that is killed mid-step leaves
// every line up to that point, so a reader has to tolerate a file with no
// run.end. That is the price of streaming (a reader never loads a whole
// trajectory) and of not rewriting a file to add one line to it.
//
// It records digests by default. Tool arguments carry API keys and tool results
// carry file contents; a format that writes them to disk by default would change
// this project's privacy posture silently. CaptureFull has to be asked for.
const (
	// TrajectoryVersion is the schema version on every line, so a consumer can
	// pin what it reads instead of guessing from the fields that are present.
	TrajectoryVersion = 1

	// Capture modes.
	TrajectoryCaptureMetadata = "metadata"
	TrajectoryCaptureFull     = "full"

	// Event types. They are the "type" field a reader switches on.
	TrajectoryRunStart = "run.start"
	TrajectoryLLMCall  = "llm.call"
	TrajectoryToolCall = "tool.call"
	TrajectoryStepEnd  = "step.end"
	TrajectoryRunEnd   = "run.end"

	// TrajectoryMaxTextBytes bounds one captured payload. A result can be
	// megabytes; a trajectory is a record, not a copy of the transcript. The
	// character count on the same object always reports the true length.
	TrajectoryMaxTextBytes = 64 << 10

	// TrajectoryMaxRuns and TrajectoryMaxBytes bound the store. Whichever is
	// reached first wins, oldest first: a trajectory is analysis data, and the
	// value of the hundredth run over the first is not worth an unbounded disk.
	TrajectoryMaxRuns  = 200
	TrajectoryMaxBytes = 64 << 20

	// runIDLayout is the file-name stem: UTC, second resolution, then the
	// in-process run counter. Timestamp-first means a directory listing is
	// already in chronological order.
	runIDLayout = "20060102T150405Z"
)

// Trajectory writes the runs of one agent. A nil Trajectory records nothing,
// which is how "off" is spelled everywhere in this package — the same shape as a
// nil PriceTable pricing nothing.
type Trajectory struct {
	dir     string
	capture string
	now     func() time.Time

	mu     sync.Mutex
	seq    int
	live   map[string]bool
	warned bool
}

// NewTrajectory returns a writer for dir, or nil when dir is empty.
//
// It does not touch the filesystem: a directory that cannot be created is
// discovered on the first run, where it becomes a warning rather than a failure
// to start.
func NewTrajectory(dir, capture string) *Trajectory {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if capture != TrajectoryCaptureFull {
		capture = TrajectoryCaptureMetadata
	}
	return &Trajectory{
		dir:     dir,
		capture: capture,
		now:     time.Now,
		live:    make(map[string]bool),
	}
}

// Dir reports the directory this writer uses. Empty for a nil writer.
func (t *Trajectory) Dir() string {
	if t == nil {
		return ""
	}
	return t.dir
}

// Capture reports the capture mode. Empty for a nil writer.
func (t *Trajectory) Capture() string {
	if t == nil {
		return ""
	}
	return t.capture
}

// RunMetadata is what a run knows about itself before it calls the model.
type RunMetadata struct {
	Session  string
	Agent    string
	Provider string
	Model    string
	Prompt   string
}

// Start opens a run: one file, and a run.start line.
//
// A failure here returns nil rather than an error, and warns once. The caller
// then records nothing for this run, and the run itself proceeds — see the file
// comment for why that is the deliberate direction.
func (t *Trajectory) Start(meta RunMetadata) *TrajectoryRun {
	if t == nil {
		return nil
	}
	run := &TrajectoryRun{
		trajectory: t,
		session:    meta.Session,
		agent:      meta.Agent,
		provider:   meta.Provider,
		model:      meta.Model,
		started:    t.now(),
	}
	file, id, err := t.create()
	if err != nil {
		t.warnOnce(err)
		return nil
	}
	run.id = id
	run.file = file
	run.writer = bufio.NewWriterSize(file, 32<<10)

	run.emit(&runStartEvent{
		trajectoryEnvelope: run.envelope(TrajectoryRunStart),
		Provider:           meta.Provider,
		Model:              meta.Model,
		Capture:            t.capture,
		Prompt:             t.payload(meta.Prompt),
	})
	return run
}

// create opens a fresh file and returns it with its run id.
//
// The name is timestamp-based, so the directory sorts by time; two runs can land
// in the same second, and two processes can start in the same second, so the
// file is created exclusively and a collision bumps a suffix instead of
// overwriting a record somebody may be analysing.
func (t *Trajectory) create() (*os.File, string, error) {
	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		return nil, "", err
	}
	base := t.now().UTC().Format(runIDLayout)

	t.mu.Lock()
	t.seq++
	seq := t.seq
	t.mu.Unlock()

	for attempt := 0; attempt < 100; attempt++ {
		id := fmt.Sprintf("%s-r%d", base, seq)
		if attempt > 0 {
			id = fmt.Sprintf("%s-r%d-%d", base, seq, attempt+1)
		}
		file, err := os.OpenFile(filepath.Join(t.dir, id+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			t.mu.Lock()
			t.live[id] = true
			t.mu.Unlock()
			return file, id, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("cannot allocate a run id in %s", t.dir)
}

// warnOnce reports a recording failure the first time only: a full disk would
// otherwise write one warning per event.
func (t *Trajectory) warnOnce(err error) {
	t.mu.Lock()
	already := t.warned
	t.warned = true
	t.mu.Unlock()
	if already {
		return
	}
	tlog.Warn("trajectory", "recording disabled", "dir", t.dir, "error", err)
}

// payload renders one string for recording: its digest and length always, its
// text only when the capture mode asks for it.
//
// A digest rather than a prefix is what makes dedup possible: two runs that
// passed the same argument to the same tool line up, and one that passed a
// different file does not, without reading either argument.
func (t *Trajectory) payload(s string) *trajectoryPayload {
	if s == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(s))
	payload := &trajectoryPayload{SHA256: hex.EncodeToString(sum[:]), Chars: len(s)}
	if t.capture == TrajectoryCaptureFull {
		text := s
		if len(text) > TrajectoryMaxTextBytes {
			text = text[:TrajectoryMaxTextBytes]
		}
		payload.Text = text
	}
	return payload
}

// TrajectoryRun is one run's file. A nil *TrajectoryRun records nothing, so
// every method below is safe to call on the nil a disabled writer returns.
type TrajectoryRun struct {
	trajectory *Trajectory
	id         string
	parent     string
	session    string
	agent      string
	provider   string
	model      string
	started    time.Time

	file   *os.File
	writer *bufio.Writer

	mu       sync.Mutex
	closed   bool
	failed   error
	childSeq int
}

// ID is the run's identity: what the file is named and what every line carries.
func (r *TrajectoryRun) ID() string {
	if r == nil {
		return ""
	}
	return r.id
}

// Failed reports why this run stopped recording, or nil. It is what the caller
// logs; a non-nil error here never changes what Run returns.
func (r *TrajectoryRun) Failed() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed
}

// envelope is the part every line shares.
func (r *TrajectoryRun) envelope(kind string) trajectoryEnvelope {
	return trajectoryEnvelope{
		Version: TrajectoryVersion,
		Type:    kind,
		Run:     r.id,
		Parent:  r.parent,
		Session: r.session,
		Agent:   r.agent,
		At:      r.trajectory.now().UTC().Format(time.RFC3339Nano),
	}
}

// emit appends one event and flushes it, so a killed run leaves its lines.
//
// The first write failure closes the run: a trajectory that keeps trying to
// write to a full disk would spend the run's time on failures.
func (r *TrajectoryRun) emit(event any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.writer == nil {
		return
	}
	data, err := json.Marshal(event)
	if err == nil {
		data = append(data, '\n')
		_, err = r.writer.Write(data)
	}
	if err == nil {
		err = r.writer.Flush()
	}
	if err != nil {
		r.failed = err
		r.closeLocked()
		r.trajectory.warnOnce(err)
	}
}

// LLMCall records one provider call. duration is measured by the caller from the
// instant the request was sent, which is also the instant its price is keyed on.
//
// ttft is the delay to the first streamed delta, zero when nothing was streamed;
// it is recorded only when it is known, because "no first token" and "the first
// token arrived at 0 ms" are different facts.
func (r *TrajectoryRun) LLMCall(step int, duration, ttft time.Duration, outcome string, usage types.Usage, reported bool, cost *types.CostEvent) {
	if r == nil {
		return
	}
	event := &llmCallEvent{
		trajectoryEnvelope: r.envelope(TrajectoryLLMCall),
		Step:               step,
		DurationMS:         duration.Milliseconds(),
		Outcome:            outcome,
	}
	if ttft > 0 {
		ms := ttft.Milliseconds()
		event.TTFTMS = &ms
	}
	if reported {
		event.Tokens = &usage
	}
	event.Cost = cost
	r.emit(event)
}

// ToolCall records one tool call: what it was, how long it took, what came back,
// and whether it failed.
//
// The caller writes these in call-index order rather than completion order, so
// the file reads the way the step is described to the model even though the
// calls ran concurrently.
func (r *TrajectoryRun) ToolCall(step, call int, tool, args, result string, duration time.Duration, status string) {
	if r == nil {
		return
	}
	r.emit(&toolCallEvent{
		trajectoryEnvelope: r.envelope(TrajectoryToolCall),
		Step:               step,
		Call:               call,
		Tool:               tool,
		Args:               r.trajectory.payload(args),
		Result:             r.trajectory.payload(result),
		DurationMS:         duration.Milliseconds(),
		Status:             status,
	})
}

// StepEnd closes one step.
func (r *TrajectoryRun) StepEnd(step int, duration time.Duration) {
	if r == nil {
		return
	}
	r.emit(&stepEndEvent{
		trajectoryEnvelope: r.envelope(TrajectoryStepEnd),
		Step:               step,
		DurationMS:         duration.Milliseconds(),
	})
}

// End closes the run: its outcome, its totals, and the file.
//
// stop is the vocabulary a reader groups by — final, budget, blocked,
// step_limit, cancelled, error — and it is why an evaluation harness can tell
// "the agent answered" from "the budget ran out before it could".
func (r *TrajectoryRun) End(stop string, steps int, duration time.Duration, usage types.Usage, cost types.CostTotals, costUnknown int) {
	if r == nil {
		return
	}
	event := &runEndEvent{
		trajectoryEnvelope: r.envelope(TrajectoryRunEnd),
		Stop:               stop,
		Steps:              steps,
		DurationMS:         duration.Milliseconds(),
		CostUnknown:        costUnknown,
	}
	if usage.TotalTokens > 0 {
		event.Tokens = &usage
	}
	if len(cost) > 0 {
		totals := make(costTotalsJSON, 0, len(cost))
		units := make([]string, 0, len(cost))
		for unit := range cost {
			units = append(units, unit)
		}
		sort.Strings(units)
		for _, unit := range units {
			totals = append(totals, costTotalJSON{Unit: unit, Amount: cost[unit]})
		}
		event.Cost = totals
	}
	r.emit(event)

	r.mu.Lock()
	r.closeLocked()
	r.mu.Unlock()
}

// closeLocked flushes and closes the file. The caller holds the lock.
func (r *TrajectoryRun) closeLocked() {
	if r.closed {
		return
	}
	r.closed = true
	if r.writer != nil {
		if err := r.writer.Flush(); err != nil && r.failed == nil {
			r.failed = err
		}
		r.writer = nil
	}
	if r.file != nil {
		if err := r.file.Close(); err != nil && r.failed == nil {
			r.failed = err
		}
		r.file = nil
	}
	if r.trajectory != nil {
		r.trajectory.mu.Lock()
		delete(r.trajectory.live, r.id)
		r.trajectory.mu.Unlock()
	}
}

// Child starts a run for a delegated agent, named after the parent so the
// relationship is visible in a directory listing as well as in the parent field.
//
// The parent hands out the number rather than the child inventing one: two
// sub-agents of the same parent start in the same second, and an id they each
// derived themselves would collide in the store.
func (r *TrajectoryRun) Child(agentName string) *TrajectoryRun {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.childSeq++
	seq := r.childSeq
	r.mu.Unlock()

	child := &TrajectoryRun{
		trajectory: r.trajectory,
		parent:     r.id,
		session:    r.session,
		agent:      agentName,
		started:    r.trajectory.now(),
	}
	name := sanitizeRunIDSegment(agentName)
	if name == "" {
		name = "child"
	}
	child.id = fmt.Sprintf("%s.%s-%d", r.id, name, seq)

	path := filepath.Join(r.trajectory.dir, child.id+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		r.trajectory.warnOnce(err)
		return nil
	}
	child.file = file
	child.writer = bufio.NewWriterSize(file, 32<<10)
	r.trajectory.mu.Lock()
	r.trajectory.live[child.id] = true
	r.trajectory.mu.Unlock()

	child.emit(&runStartEvent{
		trajectoryEnvelope: child.envelope(TrajectoryRunStart),
		Capture:            r.trajectory.capture,
	})
	return child
}

// sanitizeRunIDSegment keeps a run id to characters that need no quoting in a
// file name, since an agent name arrives from configuration.
func sanitizeRunIDSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// The wire shapes. One struct per event type rather than one with every field:
// a reader of the format documentation and a reader of this file should see the
// same table, and the compiler then keeps optionality honest.

type trajectoryEnvelope struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	Run     string `json:"run"`
	Parent  string `json:"parent,omitempty"`
	Session string `json:"session,omitempty"`
	Agent   string `json:"agent,omitempty"`
	At      string `json:"at"`
}

type trajectoryPayload struct {
	SHA256 string `json:"sha256"`
	Chars  int    `json:"chars"`
	Text   string `json:"text,omitempty"`
}

type runStartEvent struct {
	trajectoryEnvelope
	Provider string             `json:"provider,omitempty"`
	Model    string             `json:"model,omitempty"`
	Capture  string             `json:"capture"`
	Prompt   *trajectoryPayload `json:"prompt,omitempty"`
}

type llmCallEvent struct {
	trajectoryEnvelope
	Step       int              `json:"step"`
	DurationMS int64            `json:"duration_ms"`
	TTFTMS     *int64           `json:"ttft_ms,omitempty"`
	Outcome    string           `json:"outcome"`
	Tokens     *types.Usage     `json:"tokens,omitempty"`
	Cost       *types.CostEvent `json:"cost,omitempty"`
}

type toolCallEvent struct {
	trajectoryEnvelope
	Step       int                `json:"step"`
	Call       int                `json:"call"`
	Tool       string             `json:"tool"`
	Args       *trajectoryPayload `json:"args,omitempty"`
	Result     *trajectoryPayload `json:"result,omitempty"`
	DurationMS int64              `json:"duration_ms"`
	Status     string             `json:"status"`
}

type stepEndEvent struct {
	trajectoryEnvelope
	Step       int   `json:"step"`
	DurationMS int64 `json:"duration_ms"`
}

type runEndEvent struct {
	trajectoryEnvelope
	Stop        string         `json:"stop"`
	Steps       int            `json:"steps"`
	DurationMS  int64          `json:"duration_ms"`
	Tokens      *types.Usage   `json:"tokens,omitempty"`
	Cost        costTotalsJSON `json:"cost,omitempty"`
	CostUnknown int            `json:"cost_unknown_calls,omitempty"`
}

// costTotalsJSON is CostTotals as an ordered array rather than a map, so two
// runs' files diff cleanly: a map's key order is Go's, not a document's.
type costTotalsJSON []costTotalJSON

type costTotalJSON struct {
	Unit   string  `json:"unit"`
	Amount float64 `json:"amount"`
}

// Prune keeps the store inside its bounds: the newest maxRuns files, and no more
// than maxBytes in total. Runs still being written are never removed, because
// one of them is the file the caller is about to finish.
//
// It returns the number of files removed. A prune that cannot read the directory
// reports the error and removes nothing: a missing listing must not be read as
// "everything is expendable".
func (t *Trajectory) Prune(maxRuns int, maxBytes int64) (int, error) {
	if t == nil {
		return 0, nil
	}
	if maxRuns <= 0 {
		maxRuns = TrajectoryMaxRuns
	}
	if maxBytes <= 0 {
		maxBytes = TrajectoryMaxBytes
	}
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	type fileInfo struct {
		name  string
		mtime time.Time
		size  int64
	}
	files := make([]fileInfo, 0, len(entries))
	t.mu.Lock()
	live := make(map[string]bool, len(t.live))
	for id := range t.live {
		live[id] = true
	}
	t.mu.Unlock()

	var total int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue // a file that vanished between listing and stat
		}
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		if live[id] {
			total += info.Size()
			continue
		}
		files = append(files, fileInfo{name: entry.Name(), mtime: info.ModTime(), size: info.Size()})
		total += info.Size()
	}

	// Oldest first: the bound exists so the newest analysis survives, and a
	// directory listing is already chronological by name, but modtime is what a
	// copied or restored store actually reflects.
	sort.Slice(files, func(i, j int) bool {
		if !files[i].mtime.Equal(files[j].mtime) {
			return files[i].mtime.Before(files[j].mtime)
		}
		return files[i].name < files[j].name
	})

	removed := 0
	remaining := len(files)
	for _, file := range files {
		if remaining <= maxRuns && total <= maxBytes {
			break
		}
		if err := os.Remove(filepath.Join(t.dir, file.name)); err != nil {
			continue
		}
		total -= file.size
		remaining--
		removed++
	}
	return removed, nil
}
