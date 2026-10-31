package agent

import (
	"path/filepath"
	"strings"

	"github.com/yusiwen/tinycode/types"
)

// Path-level control over compaction (roadmap B5, S4).
//
// A run's messages hold the tool results, so "unload this file" is a decision
// about those. `/drop <path>` cannot make it — the messages live inside Run and
// the TUI refuses commands while a run is active — so the decision is made
// before the run instead: a kept path's reads survive compaction verbatim, and
// every other read is summarized away as usual.

// KeepPath adds a path whose tool reads must survive compaction. The path is
// cleaned and stored once; an empty path is ignored.
func (a *Agent) KeepPath(path string) (added bool) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return false
	}
	for _, kept := range a.KeptPaths {
		if kept == path {
			return false
		}
	}
	a.KeptPaths = append(a.KeptPaths, path)
	return true
}

// UnkeepPath removes a path, reporting whether it was there.
func (a *Agent) UnkeepPath(path string) (removed bool) {
	path = filepath.Clean(strings.TrimSpace(path))
	for i, kept := range a.KeptPaths {
		if kept != path {
			continue
		}
		a.KeptPaths = append(a.KeptPaths[:i], a.KeptPaths[i+1:]...)
		return true
	}
	return false
}

// keepsPath reports whether a tool call's arguments name a kept path. Arguments
// are the signal because that is where a read names the file it reads.
func (a *Agent) keepsPath(arguments string) bool {
	if arguments == "" {
		return false
	}
	for _, kept := range a.KeptPaths {
		if kept != "" && strings.Contains(arguments, kept) {
			return true
		}
	}
	return false
}

// Turn-level control over the live history (roadmap B5).
//
// A *turn* is a user message plus everything up to the next user message — the
// same grouping compressHistory works in, so "pin the last turn" and "protect
// this group from the summarizer" mean the same thing.
//
// None of these touch the session store: what is on disk is the record
// `--resume` reads, and a command that silently rewrote it would be a
// destructive edit rather than context control.

// turnStarts returns the index of every user message in history.
func turnStarts(history []types.Message) []int {
	var starts []int
	for i, msg := range history {
		if msg.Role == types.RoleUser {
			starts = append(starts, i)
		}
	}
	return starts
}

// turns returns the [start, end) range of every turn, oldest first.
func turns(history []types.Message) [][2]int {
	starts := turnStarts(history)
	ranges := make([][2]int, 0, len(starts))
	for i, start := range starts {
		end := len(history)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		ranges = append(ranges, [2]int{start, end})
	}
	return ranges
}

// lastTurns returns the ranges of the last n turns, oldest first. Asking for
// more turns than exist returns every turn rather than failing: "pin the last 5"
// on a three-turn history should do what it can and report the count.
func lastTurns(history []types.Message, n int) [][2]int {
	ranges := turns(history)
	if n <= 0 || len(ranges) == 0 {
		return nil
	}
	if n > len(ranges) {
		n = len(ranges)
	}
	return ranges[len(ranges)-n:]
}

// PinLast marks the last n turns as pinned and returns how many turns it pinned
// and how many are pinned in total.
func (a *Agent) PinLast(n int) (pinned, total int) {
	for _, r := range lastTurns(a.History, n) {
		for i := r[0]; i < r[1]; i++ {
			a.History[i].Pinned = true
		}
		pinned++
	}
	return pinned, a.PinnedTurnCount()
}

// UnpinLast clears the pin on the last n pinned turns, newest first, and returns
// how many it cleared and how many remain.
func (a *Agent) UnpinLast(n int) (unpinned, remaining int) {
	if n <= 0 {
		n = 1
	}
	ranges := turns(a.History)
	for i := len(ranges) - 1; i >= 0 && unpinned < n; i-- {
		cleared := false
		for j := ranges[i][0]; j < ranges[i][1]; j++ {
			if a.History[j].Pinned {
				a.History[j].Pinned = false
				cleared = true
			}
		}
		if cleared {
			unpinned++
		}
	}
	return unpinned, a.PinnedTurnCount()
}

// DropLast removes the last n turns from the live history and returns how many
// turns went. The session file is untouched.
func (a *Agent) DropLast(n int) (dropped int) {
	ranges := lastTurns(a.History, n)
	if len(ranges) == 0 {
		return 0
	}
	// The copy matters: history is shared with any goroutine holding a slice of
	// it, and truncating in place would overwrite what they are reading.
	a.History = append([]types.Message(nil), a.History[:ranges[0][0]]...)
	return len(ranges)
}

// PinnedTurnTexts returns the user and assistant text of every pinned turn, in
// order. A view that mirrors the history — the TUI transcript, or a session file
// — marks the same turns from this, instead of guessing at indices.
func (a *Agent) PinnedTurnTexts() [][2]string {
	var out [][2]string
	for _, r := range turns(a.History) {
		pinned := false
		var pair [2]string
		for i := r[0]; i < r[1]; i++ {
			msg := a.History[i]
			if msg.Pinned {
				pinned = true
			}
			switch msg.Role {
			case types.RoleUser:
				pair[0] += msg.Content
			case types.RoleAssistant:
				pair[1] = msg.Content
			}
		}
		if pinned {
			out = append(out, pair)
		}
	}
	return out
}

// PinnedTurnCount reports how many turns carry a pin.
func (a *Agent) PinnedTurnCount() int {
	count := 0
	for _, r := range turns(a.History) {
		for i := r[0]; i < r[1]; i++ {
			if a.History[i].Pinned {
				count++
				break
			}
		}
	}
	return count
}
