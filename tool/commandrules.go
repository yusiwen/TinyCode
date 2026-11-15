package tool

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/yusiwen/tinycode/tlog"
)

// This file is the deny list. It judges a command by what it will run — the
// program and its arguments, read by shellread.go — instead of by whether the
// command text happens to contain a pattern.
//
// It is still not a boundary, and the documentation says so: a shell can build a
// command at run time (`$(which rm)`, `eval`, a script from a pipe) and no
// reader can see through that. What the structure buys is the two failures a
// substring match cannot avoid: it stops admitting `r"m" -rf /` because the text
// no longer contains "rm -rf /", and it stops refusing `echo "sudo "` because the
// word appears in an argument rather than in the program position.

// commandRule is one built-in denial. The detail is what the refusal and the log
// call it: a phrase, not the pattern, because there is no pattern any more.
type commandRule struct {
	name  string
	match func(seg ShellSegment) bool
}

// builtinCommandRules are the commands nobody needs from a coding agent, in the
// order they are reported. Each one names a program and tests its arguments; a
// rule that cannot see the program does not fire, which is why the wrapper
// expansion in shellread.go matters.
var builtinCommandRules = []commandRule{
	{
		name: "removes the filesystem root recursively",
		match: func(seg ShellSegment) bool {
			if segProgram(seg) != "rm" {
				return false
			}
			if !(shortFlag(seg, "rR") || longFlag(seg, "--recursive")) || !(shortFlag(seg, "f") || longFlag(seg, "--force")) {
				return false
			}
			for _, arg := range operands(seg) {
				if isRootTarget(arg) {
					return true
				}
			}
			return false
		},
	},
	{
		name: "formats or repartitions a device",
		match: func(seg ShellSegment) bool {
			switch segProgram(seg) {
			case "mkfs", "fdisk", "sfdisk", "gdisk", "parted", "mkswap", "wipefs", "cfdisk":
				return true
			}
			return strings.HasPrefix(segProgram(seg), "mkfs.")
		},
	},
	{
		name: "writes a raw block device",
		match: func(seg ShellSegment) bool {
			if segProgram(seg) != "dd" {
				return false
			}
			for _, arg := range seg.Argv {
				if strings.HasPrefix(arg, "of=/dev/") {
					return true
				}
			}
			return false
		},
	},
	{
		name: "escalates privileges",
		match: func(seg ShellSegment) bool {
			switch segProgram(seg) {
			case "sudo", "su", "doas", "pkexec":
				return true
			}
			return false
		},
	},
	{
		name: "powers the machine off or reboots it",
		match: func(seg ShellSegment) bool {
			switch segProgram(seg) {
			case "shutdown", "reboot", "halt", "poweroff":
				return true
			case "init":
				for _, arg := range operands(seg) {
					if arg == "0" || arg == "6" {
						return true
					}
				}
			case "systemctl":
				for _, arg := range operands(seg) {
					switch arg {
					case "poweroff", "reboot", "halt", "suspend", "kexec":
						return true
					}
				}
			}
			return false
		},
	},
	{
		name: "changes permissions recursively on the filesystem root",
		match: func(seg ShellSegment) bool {
			switch segProgram(seg) {
			case "chmod", "chown", "chgrp":
			default:
				return false
			}
			if !(shortFlag(seg, "R") || longFlag(seg, "--recursive")) {
				return false
			}
			for _, arg := range operands(seg) {
				if isRootTarget(arg) {
					return true
				}
			}
			return false
		},
	},
	{
		name: "defines a shell function that calls itself",
		match: func(seg ShellSegment) bool {
			// The fork bomb is `:(){ :|:& };:`. The reader cannot make a
			// function definition out of it, so what it sees is the `:` program
			// with a body for arguments. The shell's no-op is not something a
			// command-line agent needs, and a function definition is the only
			// reason to spell it this way.
			if segProgram(seg) != ":" {
				return false
			}
			return len(seg.Argv) > 1
		},
	},
}

// deniedWords are the programs whose mere mention in a command the reader could
// not parse is enough to refuse it. They are all commands that escalate
// privilege, power the machine off, or rewrite a device: nobody says them in
// passing, so a false positive costs less than a false negative.
var deniedWords = []string{
	"sudo", "su", "doas", "pkexec",
	"shutdown", "reboot", "halt", "poweroff",
	"mkfs", "fdisk", "sfdisk", "gdisk", "parted", "mkswap", "wipefs",
}

// CheckCommand judges one command string against the built-in rules and the
// configured extra entries. A nil error means no rule matched — not that the
// command is safe: the kernel boundary is what makes a command safe.
func (sc *SandboxConfig) CheckCommand(cmd string) error {
	parsed := ParseCommand(cmd)
	// An unreadable command is checked by word as well: the reader gave up on
	// the structure, so a program name that cannot be said in passing is
	// enough to refuse. This is the one place a word is matched, and it is
	// reached only when there is no structure to match.
	if parsed.OpaqueCommand() {
		for _, name := range deniedWords {
			if wordMentions(parsed, name) {
				tlog.Warn("sandbox", "cmd_blocked", "rule", "unreadable command mentions "+name, "cmd", cmd)
				return fmt.Errorf("command blocked by security policy: an unreadable command mentions %q", name)
			}
		}
	}
	if name, seg := matchBuiltin(parsed); name != "" {
		tlog.Warn("sandbox", "cmd_blocked", "rule", name, "program", segProgram(seg), "cmd", cmd)
		return fmt.Errorf("command blocked by security policy: %s", name)
	}
	for _, entry := range sc.denyEntries() {
		if entry.matches(parsed) {
			tlog.Warn("sandbox", "cmd_blocked", "rule", entry.label, "cmd", cmd)
			return fmt.Errorf("command blocked by the configured command policy (matches: %q)", entry.label)
		}
	}
	return nil
}

// denyEntry is one configured deny_commands entry, read as an argv prefix.
type denyEntry struct {
	label  string
	prefix []string
	// raw is set when the entry could not be read as a command; the entry is
	// then matched as a word in the text, which over-matches rather than
	// under-matches.
	raw string
}

// denyEntries reads the configured entries once per call. The list is small and
// changing it lazily would mean a stale rule after a configuration reload.
func (sc *SandboxConfig) denyEntries() []denyEntry {
	if sc == nil {
		return nil
	}
	out := make([]denyEntry, 0, len(sc.DenyCommands))
	for _, raw := range sc.DenyCommands {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			// An empty pattern used to match every command, which turned one
			// stray comma in a config file into "nothing runs".
			continue
		}
		parsed := ParseCommand(entry)
		if parsed.OpaqueCommand() || len(parsed.Segments) == 0 || len(parsed.Segments[0].Argv) == 0 {
			out = append(out, denyEntry{label: entry, raw: baseProgram(entry)})
			continue
		}
		out = append(out, denyEntry{label: entry, prefix: parsed.Segments[0].Argv})
	}
	return out
}

// matches reports whether the entry denies any simple command in the input.
func (e denyEntry) matches(parsed ShellCommand) bool {
	if e.raw != "" {
		return wordMentions(parsed, e.raw)
	}
	for _, seg := range parsed.Segments {
		if argvHasPrefix(seg.Argv, e.prefix) {
			return true
		}
	}
	return false
}

// matchBuiltin returns the first built-in rule that fires.
func matchBuiltin(parsed ShellCommand) (string, ShellSegment) {
	for _, seg := range parsed.Segments {
		for _, rule := range builtinCommandRules {
			if rule.match(seg) {
				return rule.name, seg
			}
		}
	}
	return "", ShellSegment{}
}

// wordMentions is the fallback for a command the reader could not classify: it
// reports whether the text mentions name as a word. It is not the substring
// match this file replaces — the word boundaries are the point — and it is used
// only where structure is unavailable, so an opaque `eval "$X"` cannot drop a
// denial that the reader never got the chance to see.
func wordMentions(parsed ShellCommand, name string) bool {
	if name == "" {
		return false
	}
	if parsed.Raw == "" {
		return false
	}
	re := regexp.MustCompile(`(^|[^A-Za-z0-9_.-])` + regexp.QuoteMeta(name) + `([^A-Za-z0-9_.-]|$)`)
	return re.MatchString(parsed.Raw)
}

// segProgram returns the program name a rule matches on.
func segProgram(seg ShellSegment) string {
	if len(seg.Argv) == 0 {
		return ""
	}
	return baseProgram(seg.Argv[0])
}

// operands returns the arguments that are not options.
func operands(seg ShellSegment) []string {
	var out []string
	for _, arg := range seg.Argv[1:] {
		if strings.HasPrefix(arg, "-") && len(arg) > 1 {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// shortFlag reports whether a combined short option carries any of the letters
// (`-rf` carries r and f).
func shortFlag(seg ShellSegment, letters string) bool {
	for _, arg := range seg.Argv[1:] {
		if len(arg) < 2 || arg[0] != '-' || strings.HasPrefix(arg, "--") {
			continue
		}
		for _, r := range arg[1:] {
			if strings.ContainsRune(letters, r) {
				return true
			}
		}
	}
	return false
}

// longFlag reports whether the long form of an option is present.
func longFlag(seg ShellSegment, name string) bool {
	for _, arg := range seg.Argv[1:] {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

// isRootTarget reports whether a target names a filesystem root, including the
// glob spellings (`/`, `/*`, `/**`) that mean the same thing to a shell.
//
// Both separators are read: the command may be POSIX (`rm -rf /`, which Git Bash
// on Windows resolves to its own root) or Windows (`rm -rf C:/`). The comparison
// is against slash-normalised text with the path package, because filepath.Clean
// on Windows turns "/" into "\" and the rule would silently stop matching the
// most destructive command there is.
func isRootTarget(target string) bool {
	if target == "" || strings.Contains(target, dynamicWord) {
		return false
	}
	p := strings.ReplaceAll(target, `\`, "/")
	// `C:`, `C:/` and `c:\` all name the top of a drive. Over-matching here is
	// the safe direction: a false positive costs one refused command, and the
	// command text may be resolved by either convention depending on the shell.
	if len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) && (len(p) == 2 || strings.Trim(p[2:], "/") == "") {
		return true
	}
	clean := path.Clean(p)
	if clean == "/" {
		return true
	}
	return strings.HasPrefix(clean, "/") && strings.Trim(clean, "/*") == ""
}

// isASCIILetter reports whether c is an ASCII letter, the only thing Windows
// accepts as a drive letter.
func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// argvHasPrefix reports whether argv starts with prefix, comparing program names
// by their last path element so `curl` and `/usr/bin/curl` are the same entry.
func argvHasPrefix(argv, prefix []string) bool {
	if len(prefix) == 0 || len(argv) < len(prefix) {
		return false
	}
	if baseProgram(argv[0]) != baseProgram(prefix[0]) {
		return false
	}
	for i := 1; i < len(prefix); i++ {
		if argv[i] != prefix[i] {
			return false
		}
	}
	return true
}
