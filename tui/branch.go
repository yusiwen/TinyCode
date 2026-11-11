package tui

import (
	"fmt"
	"strings"

	"github.com/yusiwen/tinycode/session"
)

// sessionTreeText renders the branch forest for /session: the same shape
// --list-sessions shows, marked so the branch this TUI is on is visible, and
// with the fork point where the CLI shows the last-active time.
//
// The tree is the point of the view: before it, a branch could only be told
// apart from its parent by decoding the id, which is not the authoritative link
// (roadmap H3).
func sessionTreeText(store *session.Store, current string) string {
	roots := store.Tree()
	if len(roots) == 0 {
		return "No saved sessions."
	}
	var b strings.Builder
	b.WriteString("Sessions and branches:\n")
	session.Walk(roots, func(n *session.Node) {
		mark := " "
		if n.ID == current {
			mark = "*"
		}
		title := n.Title
		if title == "" {
			title = "(no title)"
		}
		detail := fmt.Sprintf("%d msgs", n.MessageCount)
		switch {
		case n.Orphan:
			detail += fmt.Sprintf(", parent %s missing", n.ParentSessionID)
		case n.Cycle:
			detail += fmt.Sprintf(", parent %s: cycle broken", n.ParentSessionID)
		case n.ParentSessionID != "":
			detail += fmt.Sprintf(", forked at %d", n.ForkAt)
		}
		// The guides go inside the id column so the titles line up at every depth.
		fmt.Fprintf(&b, "%s %-34s %s (%s)\n", mark, n.Indent+n.Connector+n.ID, title, detail)
	})
	return strings.TrimRight(b.String(), "\n")
}

// formatComparison renders /compare: where the two branch chains meet, and where
// their transcripts stop agreeing. The two are reported separately because they
// can disagree — a fork copies its parent's transcript, but a drop, a
// compaction or an edited file can leave the records saying something the link
// does not.
func formatComparison(c *session.Comparison) string {
	var b strings.Builder
	b.WriteString("Branch comparison\n")
	fmt.Fprintf(&b, "  a  %-30s %s (%d msgs)\n", c.A.ID, branchTitle(c.A.Title), c.A.MessageCount)
	fmt.Fprintf(&b, "  b  %-30s %s (%d msgs)\n", c.B.ID, branchTitle(c.B.Title), c.B.MessageCount)

	switch {
	case c.CommonAncestor == "":
		b.WriteString("  common ancestor  none: the two sessions are not related\n")
	case c.AncestorOf == "a":
		fmt.Fprintf(&b, "  common ancestor  %s: a is an ancestor of b\n", c.CommonAncestor)
	case c.AncestorOf == "b":
		fmt.Fprintf(&b, "  common ancestor  %s: b is an ancestor of a\n", c.CommonAncestor)
	default:
		fmt.Fprintf(&b, "  common ancestor  %s: siblings\n", c.CommonAncestor)
	}
	for _, f := range c.Forks {
		fmt.Fprintf(&b, "  fork             %s forked at message %d\n", f.ID, f.At)
	}
	noun := "messages"
	if c.CommonPrefix == 1 {
		noun = "message"
	}
	fmt.Fprintf(&b, "  common prefix    %d %s identical\n", c.CommonPrefix, noun)
	switch {
	case c.AFirstDiff == "" && c.BFirstDiff == "":
		b.WriteString("  transcripts      identical\n")
	default:
		if c.AFirstDiff == "" {
			fmt.Fprintf(&b, "  a ends at        message %d\n", c.CommonPrefix)
		} else {
			fmt.Fprintf(&b, "  a then           %s\n", c.AFirstDiff)
		}
		if c.BFirstDiff == "" {
			fmt.Fprintf(&b, "  b ends at        message %d\n", c.CommonPrefix)
		} else {
			fmt.Fprintf(&b, "  b then           %s\n", c.BFirstDiff)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func branchTitle(title string) string {
	if title == "" {
		return "(no title)"
	}
	return title
}
