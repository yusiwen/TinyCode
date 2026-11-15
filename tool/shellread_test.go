package tool

import (
	"strings"
	"testing"
)

// TestParseCommandReadsWhatTheRulesNeed is the reader's contract: the programs a
// rule will see, the files a command writes, and whether anything was left
// unclassified. Every case here is a command a person or a model actually wrote,
// or an evasion the substring matching this replaces got wrong.
func TestParseCommandReadsWhatTheRulesNeed(t *testing.T) {
	cases := []struct {
		name     string
		cmd      string
		programs []string
		outputs  []string
		opaque   bool
		via      []string
	}{
		{name: "plain", cmd: "rm -rf /", programs: []string{"rm"}},
		{name: "quoted program", cmd: `r"m" -rf /`, programs: []string{"rm"}},
		{name: "ansi-c program", cmd: `$'\x72\x6d' -rf /`, programs: []string{"rm"}},
		{name: "ifs expansion splits a word", cmd: `rm${IFS}-rf /`, programs: []string{"rm"}},
		{name: "ifs alone", cmd: `rm ${IFS} -rf /`, programs: []string{"rm"}},
		{name: "assignment prefix", cmd: `FOO=1 rm -rf /`, programs: []string{"rm"}},
		{name: "shell -c", cmd: `sh -c 'rm -rf /'`, programs: []string{"rm"}, via: []string{"sh", "-c"}},
		{name: "bash -c", cmd: `bash -lc 'rm -rf /'`, programs: []string{"rm"}, via: []string{"bash", "-c"}},
		{name: "xargs", cmd: `xargs rm -rf /`, programs: []string{"rm"}, via: []string{"xargs"}},
		{name: "env", cmd: `env -i PATH=/bin rm -rf /`, programs: []string{"rm"}, via: []string{"env"}},
		{name: "nohup", cmd: `nohup rm -rf /`, programs: []string{"rm"}, via: []string{"nohup"}},
		{name: "timeout", cmd: `timeout 5 rm -rf /`, programs: []string{"rm"}, via: []string{"timeout"}},
		{name: "nice", cmd: `nice -n 10 rm -rf /`, programs: []string{"rm"}, via: []string{"nice"}},
		{name: "command", cmd: `command rm -rf /`, programs: []string{"rm"}, via: []string{"command"}},
		{name: "subshell", cmd: `(cd / && rm -rf x)`, programs: []string{"cd", "rm"}},
		{name: "substitution", cmd: `echo $(rm -rf x)`, programs: []string{"rm", "echo"}},
		{name: "backticks", cmd: "echo `rm -rf x`", programs: []string{"rm", "echo"}},
		{name: "redirect", cmd: `echo hi > out.txt`, programs: []string{"echo"}, outputs: []string{"out.txt"}},
		{name: "append", cmd: `echo hi >> out.txt`, programs: []string{"echo"}, outputs: []string{"out.txt"}},
		{name: "stderr redirect", cmd: `ls 2> err.txt`, programs: []string{"ls"}, outputs: []string{"err.txt"}},
		{name: "dev null is not a file", cmd: `ls > /dev/null`, programs: []string{"ls"}},
		{name: "fd duplication", cmd: `ls 2>&1`, programs: []string{"ls"}},
		{name: "pipe", cmd: `echo hi | tee out.txt`, programs: []string{"echo", "tee"}},
		{name: "and or", cmd: `a && b || c`, programs: []string{"a", "b", "c"}},
		{name: "semicolon", cmd: `a ; b`, programs: []string{"a", "b"}},
		{name: "heredoc body is not a command", cmd: "cat <<'EOF'\nrm -rf /\nEOF", programs: []string{"cat"}},
		{name: "quoted argument is not the program", cmd: `echo "sudo "`, programs: []string{"echo"}},
		{name: "unterminated quote is opaque", cmd: `echo "x`, programs: []string{"echo"}, opaque: true},
		{name: "eval is opaque", cmd: `eval "$X"`, opaque: true, programs: []string{"eval"}},
		{name: "source is opaque", cmd: `source ./setup.sh`, opaque: true, programs: []string{"source"}},
		{name: "dynamic program", cmd: `$CMD --version`, programs: []string{dynamicWord}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parsed := ParseCommand(c.cmd)
			var programs []string
			for _, seg := range parsed.Segments {
				if len(seg.Argv) > 0 {
					programs = append(programs, seg.Argv[0])
				}
			}
			if strings.Join(programs, ",") != strings.Join(c.programs, ",") {
				t.Errorf("programs = %q, want %q", programs, c.programs)
			}
			var outputs []string
			for _, seg := range parsed.Segments {
				outputs = append(outputs, seg.Outputs...)
			}
			if strings.Join(outputs, ",") != strings.Join(c.outputs, ",") {
				t.Errorf("outputs = %q, want %q", outputs, c.outputs)
			}
			if got := parsed.OpaqueCommand(); got != c.opaque {
				t.Errorf("opaque = %v, want %v (%v)", got, c.opaque, parsed.Opaque)
			}
			if len(c.via) > 0 {
				var first ShellSegment
				for _, seg := range parsed.Segments {
					if len(seg.Argv) > 0 && baseProgram(seg.Argv[0]) == c.programs[0] {
						first = seg
						break
					}
				}
				if strings.Join(first.Via, " ") != strings.Join(c.via, " ") {
					t.Errorf("via = %q, want %q", first.Via, c.via)
				}
			}
		})
	}
}

// TestParseCommandDoesNotReadAHeredocAsCommands is the case a line-oriented
// check gets wrong: the body is data, and a rule must not run on it.
func TestParseCommandDoesNotReadAHeredocAsCommands(t *testing.T) {
	parsed := ParseCommand("cat > file.txt <<'EOF'\nrm -rf /\nsudo reboot\nEOF\n")
	if len(parsed.Segments) != 1 {
		t.Fatalf("heredoc body was read as commands: %v", parsed.Segments)
	}
	seg := parsed.Segments[0]
	if baseProgram(seg.Argv[0]) != "cat" {
		t.Errorf("program = %q, want cat", seg.Argv[0])
	}
	if len(seg.Outputs) != 1 || seg.Outputs[0] != "file.txt" {
		t.Errorf("outputs = %v, want [file.txt]", seg.Outputs)
	}
	if !seg.Heredoc {
		t.Error("the segment does not record its heredoc")
	}
}

// TestParseCommandKeepsDynamicWordsDistinct guards the marker the rules rely on:
// an argument the reader cannot know must not compare equal to a literal path.
func TestParseCommandKeepsDynamicWordsDistinct(t *testing.T) {
	parsed := ParseCommand("rm -rf $TARGET")
	if len(parsed.Segments) != 1 || len(parsed.Segments[0].Argv) != 3 {
		t.Fatalf("argv = %v", parsed.Segments)
	}
	arg := parsed.Segments[0].Argv[2]
	if !strings.Contains(arg, dynamicWord) {
		t.Fatalf("the expanded argument lost its marker: %q", arg)
	}
	if isRootTarget(arg) {
		t.Error("an unknown argument was treated as the filesystem root")
	}
}
