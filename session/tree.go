package session

import (
	"fmt"
	"sort"
	"strings"

	"github.com/yusiwen/tinycode/types"
)

// Node is one session in the branch forest: the session's summary plus where it
// sits below the session it forked from.
//
// A branch records its origin in the session file (ParentSessionID and ForkAt),
// so the forest is derived, never reconstructed from the id — `Fork` happens to
// name a branch after its parent, but a renamed file or a session created by
// hand carries the same fields and does not follow that naming rule.
type Node struct {
	SessionInfo

	// Parent is the resolved parent id: empty for a root, and also empty for an
	// orphan (whose ParentSessionID names a file that is not in the store).
	Parent string
	// Depth is the number of parent links above this session, 0 for a root.
	Depth int
	// Orphan marks a session whose stored parent is missing from the store or
	// points at the session itself. The session is drawn at the top level
	// because there is nothing to hang it under.
	Orphan bool
	// Cycle marks the one session of a parent cycle whose link was dropped to
	// turn the cycle into a tree. At most one session per cycle carries it, so
	// the whole component stays visible.
	Cycle bool

	Children []*Node

	// Indent and Connector are the branch guides to print before the label.
	// Walk fills them; a root has an empty Connector so its label starts flush,
	// every other node carries the "├─ " or "└─ " that joins it to its parent.
	Indent    string
	Connector string
	parent    *Node
}

// Tree returns the branch forest of the store: every readable session exactly
// once, a child below its parent, siblings oldest first, roots in the same
// order List uses.
//
// Two deformations are possible in a store that is edited by hand, and both are
// shown rather than silently repaired:
//
//   - a parent link to a session that is not in the store (or to itself) makes
//     the session a root marked Orphan;
//   - a parent cycle has exactly one of its links dropped (the member with the
//     smallest id, so the choice is deterministic and repeatable) and that
//     session becomes a root marked Cycle. Every member stays reachable.
//
// A file that cannot be read is skipped, exactly as List skips it: the tree is a
// view, and one corrupt file must not take the whole view away. `Load` reports
// the error for a specific id.
func (st *Store) Tree() []*Node {
	infos := st.infos()
	nodes := make(map[string]*Node, len(infos))
	order := make([]string, 0, len(infos))
	for _, info := range infos {
		nodes[info.ID] = &Node{SessionInfo: info}
		order = append(order, info.ID)
	}

	// Resolve the parent link, then break cycles. Each session has at most one
	// parent, so the parent links form a functional graph: the components are
	// disjoint and each holds at most one cycle.
	parentOf := make(map[string]string, len(infos))
	for _, info := range infos {
		pid := info.ParentSessionID
		if pid == "" || pid == info.ID {
			continue
		}
		if _, ok := nodes[pid]; !ok {
			continue
		}
		parentOf[info.ID] = pid
	}
	resolved := make(map[string]bool, len(infos))
	for _, id := range order {
		if resolved[id] {
			continue
		}
		path := make([]string, 0, 4)
		seen := make(map[string]int, 4)
		cur := id
		for {
			if resolved[cur] {
				break
			}
			if i, ok := seen[cur]; ok {
				// path[i:] is the cycle; drop the link of its smallest id.
				breakID := path[i]
				for _, member := range path[i:] {
					if member < breakID {
						breakID = member
					}
				}
				delete(parentOf, breakID)
				nodes[breakID].Cycle = true
				break
			}
			seen[cur] = len(path)
			path = append(path, cur)
			next, ok := parentOf[cur]
			if !ok {
				break
			}
			cur = next
		}
		for _, p := range path {
			resolved[p] = true
		}
	}

	// Attach, or record why a session cannot be attached.
	var roots []*Node
	for _, id := range order {
		n := nodes[id]
		pid, linked := parentOf[id]
		switch {
		case !linked:
			roots = append(roots, n)
		default:
			n.parent = nodes[pid]
			n.Parent = pid
			n.parent.Children = append(n.parent.Children, n)
		}
		if info := n.SessionInfo; info.ParentSessionID != "" && info.ParentSessionID != n.Parent {
			// The stored link did not survive: either it names a file that is
			// not there, or a cycle was broken here.
			if !n.Cycle {
				n.Orphan = true
			}
		}
	}

	for _, n := range nodes {
		if len(n.Children) > 1 {
			sort.SliceStable(n.Children, func(i, j int) bool {
				ci, cj := n.Children[i].CreatedAt, n.Children[j].CreatedAt
				if !ci.Equal(cj) {
					return ci.Before(cj)
				}
				return n.Children[i].ID < n.Children[j].ID
			})
		}
	}
	sortRoots(roots)
	return roots
}

// sortRoots orders roots the way List orders sessions: least recently updated
// first, so the newest entry is closest to the prompt in a terminal.
func sortRoots(roots []*Node) {
	sort.SliceStable(roots, func(i, j int) bool {
		ui, uj := roots[i].UpdatedAt, roots[j].UpdatedAt
		if !ui.Equal(uj) {
			return ui.Before(uj)
		}
		return roots[i].ID < roots[j].ID
	})
}

// Walk visits every session of the forest in pre-order, filling in Indent,
// Connector and Depth so a caller can render its own line as
// Indent+Connector+text. The caller owns the text because the two surfaces show
// different columns; the guides and the order come from here so the CLI and the
// TUI cannot disagree about the shape of the tree.
func Walk(roots []*Node, visit func(*Node)) {
	var walk func(n *Node, indent string, last bool, depth int)
	walk = func(n *Node, indent string, last bool, depth int) {
		n.Depth = depth
		n.Indent = indent
		switch {
		case depth == 0:
			n.Connector = ""
		case last:
			n.Connector = "└─ "
		default:
			n.Connector = "├─ "
		}
		visit(n)

		childIndent := indent
		if depth > 0 {
			if last {
				childIndent += "   "
			} else {
				childIndent += "│  "
			}
		}
		for i, c := range n.Children {
			walk(c, childIndent, i == len(n.Children)-1, depth+1)
		}
	}
	for i, r := range roots {
		walk(r, "", i == len(roots)-1, 0)
	}
}

// Comparison is the relation between two sessions: where their branch chains
// meet, and where their transcripts stop agreeing.
//
// The two answers are independent on purpose. A fork copies the parent's
// transcript, so "the branch started at message 7" and "the two files agree on
// their first 7 messages" usually coincide — but a `/drop`, a compaction or a
// hand-edited file can make the records disagree with the link, and reporting
// both is what lets a reader see that instead of trusting the link.
type Comparison struct {
	A, B SessionInfo

	// CommonAncestor is the id of the nearest session that is an ancestor of
	// both (a session counts as its own ancestor). Empty when they share none.
	CommonAncestor string
	// AncestorOf is "a" or "b" when that side is an ancestor of the other, and
	// empty when the two merely share a higher session.
	AncestorOf string
	// Forks lists the sessions directly below the common ancestor on the way to
	// each side, with the message index each records as its fork point.
	Forks []Fork

	// CommonPrefix is the number of leading messages the two transcripts share,
	// compared by what the model sees: role, name, tool-call identity and
	// content. Reasoning is not compared because it is not resent.
	CommonPrefix int
	// AFirstDiff and BFirstDiff describe the first message that differs on each
	// side, or are empty when that side has no message there (it is a prefix of
	// the other).
	AFirstDiff string
	BFirstDiff string
}

// Fork is one session's recorded departure from its parent.
type Fork struct {
	ID string
	At int
}

// Compare relates two sessions. It refuses to compare a session with itself:
// there is no branch relation to report, and an empty comparison reads like a
// failed one.
func (st *Store) Compare(aID, bID string) (*Comparison, error) {
	if aID == bID {
		return nil, fmt.Errorf("compare needs two different sessions, got %q twice", aID)
	}
	a, err := st.Load(aID)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", aID, err)
	}
	b, err := st.Load(bID)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", bID, err)
	}
	infos := st.infos()
	byID := make(map[string]SessionInfo, len(infos)+2)
	for _, info := range infos {
		byID[info.ID] = info
	}
	byID[aID] = a.info()
	byID[bID] = b.info()

	cmp := &Comparison{A: byID[aID], B: byID[bID]}

	// Branch chain: the session, then its parent, and so on to the top.
	chain := func(id string) []string {
		var out []string
		seen := map[string]bool{}
		for id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
			id = byID[id].ParentSessionID
		}
		return out
	}
	chainA, chainB := chain(aID), chain(bID)
	indexIn := func(chain []string, id string) int {
		for i, got := range chain {
			if got == id {
				return i
			}
		}
		return -1
	}
	addFork := func(id string) {
		cmp.Forks = append(cmp.Forks, Fork{ID: id, At: byID[id].ForkAt})
	}
	inA := make(map[string]bool, len(chainA))
	for _, id := range chainA {
		inA[id] = true
	}
	for i, id := range chainB {
		if !inA[id] {
			continue
		}
		cmp.CommonAncestor = id
		j := indexIn(chainA, id)
		switch id {
		case aID:
			// a is the ancestor: b's first step below it is the fork to report.
			cmp.AncestorOf = "a"
			if i > 0 {
				addFork(chainB[i-1])
			}
		case bID:
			cmp.AncestorOf = "b"
			if j > 0 {
				addFork(chainA[j-1])
			}
		default:
			// Siblings: one step below the shared ancestor on each side.
			if i > 0 {
				addFork(chainB[i-1])
			}
			if j > 0 {
				addFork(chainA[j-1])
			}
		}
		break
	}

	// Transcript agreement, independent of the links.
	common := 0
	for common < len(a.Messages) && common < len(b.Messages) &&
		fingerprint(a.Messages[common]) == fingerprint(b.Messages[common]) {
		common++
	}
	cmp.CommonPrefix = common
	if common < len(a.Messages) {
		cmp.AFirstDiff = describeMessage(a.Messages[common])
	}
	if common < len(b.Messages) {
		cmp.BFirstDiff = describeMessage(b.Messages[common])
	}
	return cmp, nil
}

// fingerprint is what two transcripts are compared on: the fields the model is
// sent. It is written with separators that cannot appear in the fields
// themselves, so two different messages cannot produce the same string by
// concatenation.
func fingerprint(m types.Message) string {
	var b strings.Builder
	b.WriteString(m.Role)
	b.WriteByte(0)
	b.WriteString(m.Name)
	b.WriteByte(0)
	b.WriteString(m.ToolCallID)
	b.WriteByte(0)
	b.WriteString(m.Content)
	for _, tc := range m.ToolCalls {
		b.WriteByte(0)
		b.WriteString(tc.ID)
		b.WriteByte(0)
		b.WriteString(tc.Name)
		b.WriteByte(0)
		b.WriteString(tc.Arguments)
	}
	return b.String()
}

// describeMessage is the one-line summary the comparison shows. Newlines are
// flattened so a multi-line message cannot break the report's shape.
func describeMessage(m types.Message) string {
	text := strings.Join(strings.Fields(m.Content), " ")
	if len(m.ToolCalls) > 0 {
		names := make([]string, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			names = append(names, tc.Name)
		}
		if text == "" {
			text = "calls " + strings.Join(names, ", ")
		} else {
			text += " (calls " + strings.Join(names, ", ") + ")"
		}
	}
	return fmt.Sprintf("%s: %q", m.Role, truncate(text, 72))
}
