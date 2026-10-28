package agent

import "github.com/yusiwen/tinycode/types"

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
	return pinned, a.PinnedTurns()
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
	return unpinned, a.PinnedTurns()
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

// PinnedTurns reports how many turns carry a pin.
func (a *Agent) PinnedTurns() int {
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
