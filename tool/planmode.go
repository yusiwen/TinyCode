package tool

import (
	"fmt"

	"github.com/yusiwen/tinycode/types"
)

// This file answers the two questions a read-only run has to answer before it
// runs a shell command: is there a boundary to run it inside, and does the
// command write anyway?
//
// The first question is the guarantee. The second is a courtesy: it turns a
// kernel "operation not permitted" into a refusal that names the file and the
// mode, and it only covers what the reader in shellread.go can see. Nothing here
// is what makes plan mode read-only — sandboxexec.go's boundary is.

// readOnlyBoundary reports whether a read-only run's commands can run inside a
// boundary, and names what is missing when they cannot.
//
// A container tier counts as a boundary because the container invocation mounts
// the project read-only, and it probes the runtime when it starts: a run whose
// tier asks for it fails closed rather than falling back to an unconfined shell.
func readOnlyBoundary(policy types.SandboxPolicy) (ok bool, missing string) {
	if policy.Tier == types.SandboxTierContainer {
		return true, ""
	}
	if !ConfineCommands() {
		return false, "sandbox.confine_commands is false, so this command would run unconfined"
	}
	if !CommandConfinementAvailable() {
		return false, "this host has no kernel file boundary for subprocesses"
	}
	return true, ""
}

// writePrograms are the commands that write even with no redirection in sight.
// The list is deliberately short: it exists to explain the common refusal, not
// to decide whether a write is possible. A program the reader does not know is
// allowed through to the boundary, which is the thing that actually decides.
var writePrograms = map[string]bool{
	"mkdir": true, "rmdir": true, "rm": true, "mv": true, "cp": true,
	"dd": true, "chmod": true, "chown": true, "chgrp": true, "ln": true,
	"install": true, "touch": true, "truncate": true, "tee": true,
	"mount": true, "umount": true, "mkfs": true, "mkswap": true, "wipefs": true,
}

// readOnlyWriteHint names the first construct in cmd that would write. It
// returns nothing for a command whose writes it cannot see, and that is the
// intended behaviour: the boundary refuses those.
func readOnlyWriteHint(cmd string) string {
	parsed := ParseCommand(cmd)
	for _, seg := range parsed.Segments {
		if len(seg.Outputs) > 0 {
			return fmt.Sprintf("rule: output redirection writes to %s", seg.Outputs[0])
		}
		prog := segProgram(seg)
		if writePrograms[prog] || (len(prog) > 5 && prog[:5] == "mkfs.") {
			if where := viaSuffix(seg); where != "" {
				return fmt.Sprintf("rule: %s writes to disk (%s)", prog, where)
			}
			return fmt.Sprintf("rule: %s writes to disk", prog)
		}
	}
	return ""
}

// viaSuffix names the wrapper chain a segment came through, so a refusal for
// `sh -c 'mkdir x'` says it was the inner command.
func viaSuffix(seg ShellSegment) string {
	if len(seg.Via) == 0 {
		return ""
	}
	out := "via"
	for _, v := range seg.Via {
		out += " " + v
	}
	return out
}
