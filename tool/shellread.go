package tool

import (
	"path/filepath"
	"strings"
)

// This file reads a shell command string into the simple commands it will run,
// so the checks in commandrules.go can ask about programs and arguments instead
// of about substrings.
//
// The reader is deliberately conservative. Shell is a language whose meaning can
// depend on variables, files and the environment, so a reader that guessed would
// be a worse guard than the substring match it replaces: it would produce a
// confident "allowed" for a command whose real program it never saw. Every
// construct it cannot classify is recorded in Opaque with the reason, and the
// rules treat an opaque command as one they could not clear.
//
// What it resolves: quoting and escapes (r"m", $'\x72\x6d'), ${IFS}/$IFS as word
// breaks outside quotes, separators (; && || | & newlines), subshells, leading
// VARIABLE=value assignments, redirections with their targets, heredocs
// (including their bodies, which are not read as commands), command and process
// substitution, and the wrapper programs that run another program (env, command,
// exec, nohup, nice, time, stdbuf, timeout, xargs, and sh -c and its siblings).

// ShellCommand is a command string read as the simple commands it runs.
type ShellCommand struct {
	// Raw is the command as it was passed in, used only where the reader could
	// not classify a construct and the rules have to fall back to words.
	Raw string
	// Segments are the simple commands in order, including the ones produced by
	// wrappers and by command substitution: a rule can match any of them, which
	// is what lets `xargs rm -rf /` be denied for its `rm`.
	Segments []ShellSegment
	// Opaque names what the reader could not classify. A command with any of
	// these is not cleared; commandrules.go decides what that costs.
	Opaque []string
}

// OpaqueCommand reports whether anything was left unclassified.
func (c ShellCommand) OpaqueCommand() bool { return len(c.Opaque) > 0 }

// ShellSegment is one simple command: a program, its arguments, and where its
// output goes.
type ShellSegment struct {
	Argv []string
	// Outputs are the redirect targets that name a real file. A redirect to
	// /dev/null or a file-descriptor duplication is not a file write and is not
	// listed.
	Outputs []string
	// Heredoc is true when the segment carries a heredoc body.
	Heredoc bool
	// Dynamic is true when the program name came from an expansion (`$(which
	// rm)`, `$CMD`), so no rule can tell what will run.
	Dynamic bool
	// Via names the wrapper chain that produced this segment, so a message can
	// say which command inside the command was refused.
	Via []string
}

// dynamicWord marks a value the reader cannot know. It is not valid UTF-8 on
// purpose: no real argument contains it, so a rule comparing against a literal
// cannot match it by accident.
const dynamicWord = "\x00"

// ifsBreak marks the field separator ${IFS} contributed outside quotes: the word
// ends there, which is how `rm${IFS}-rf /` means `rm -rf /`.
const ifsBreak = "\x01"

// ParseCommand reads a shell command string. It never fails: what it could not
// read is reported in ShellCommand.Opaque.
func ParseCommand(cmd string) ShellCommand {
	r := &shellReader{src: cmd}
	r.run()
	r.endSegment()
	return ShellCommand{Raw: cmd, Segments: r.segments, Opaque: r.opaque}
}

type shellReader struct {
	src    string
	pos    int
	opaque []string

	segments []ShellSegment

	argv    []string
	outputs []string
	heredoc bool

	word     []byte
	inWord   bool
	segDyn   bool
	inDouble bool
}

func (r *shellReader) run() {
	for r.pos < len(r.src) {
		c := r.src[r.pos]
		switch {
		case c == '\\' && r.pos+1 < len(r.src) && r.src[r.pos+1] == '\n':
			r.pos += 2 // line continuation
		case c == '\\':
			r.readEscape()
		case c == '\'':
			r.readSingleQuoted()
		case c == '"':
			r.readDoubleQuoted()
		case c == '$' && strings.HasPrefix(r.src[r.pos:], "$'"):
			r.pos += 2
			r.readANSIQuoted()
		case c == '$':
			r.readDollar()
		case c == '`':
			r.readBacktick()
		case c == '\n':
			r.pos++
			r.endSegment()
		case c == ';', c == '&', c == '|':
			r.readOperator()
		case c == '(' || c == ')' || c == '{' || c == '}':
			// A subshell or group boundary is not part of an argument; treat it
			// as a separator so `(cd / && rm -rf x)` reads as its commands.
			r.pos++
			r.endSegment()
		case c == '>', c == '<':
			r.readRedirect()
		case c == ' ' || c == '\t':
			r.pos++
			r.endWord()
		case c == '#' && !r.inWord:
			for r.pos < len(r.src) && r.src[r.pos] != '\n' {
				r.pos++
			}
		default:
			r.word = append(r.word, c)
			r.inWord = true
			r.pos++
		}
	}
}

func (r *shellReader) readEscape() {
	if r.pos+1 >= len(r.src) {
		r.opaque = append(r.opaque, "trailing backslash")
		r.pos++
		return
	}
	r.word = append(r.word, r.src[r.pos+1])
	r.inWord = true
	r.pos += 2
}

func (r *shellReader) readSingleQuoted() {
	end := strings.IndexByte(r.src[r.pos+1:], '\'')
	if end < 0 {
		r.opaque = append(r.opaque, "unterminated single quote")
		r.pos = len(r.src)
		return
	}
	r.word = append(r.word, r.src[r.pos+1:r.pos+1+end]...)
	r.inWord = true
	r.pos += end + 2
}

func (r *shellReader) readDoubleQuoted() {
	r.pos++
	r.inWord = true
	r.inDouble = true
	defer func() { r.inDouble = false }()
	for r.pos < len(r.src) {
		switch c := r.src[r.pos]; {
		case c == '"':
			r.pos++
			return
		case c == '\\' && r.pos+1 < len(r.src):
			r.word = append(r.word, r.src[r.pos+1])
			r.pos += 2
		case c == '$':
			r.readDollar()
		case c == '`':
			r.readBacktick()
		default:
			r.word = append(r.word, c)
			r.pos++
		}
	}
	r.opaque = append(r.opaque, "unterminated double quote")
}

// readANSIQuoted decodes $'...' (ANSI-C quoting), so $'\x72\x6d' reads as "rm".
func (r *shellReader) readANSIQuoted() {
	r.inWord = true
	for r.pos < len(r.src) {
		c := r.src[r.pos]
		if c == '\'' {
			r.pos++
			return
		}
		if c != '\\' || r.pos+1 >= len(r.src) {
			r.word = append(r.word, c)
			r.pos++
			continue
		}
		switch n := r.src[r.pos+1]; n {
		case 'n':
			r.word = append(r.word, '\n')
			r.pos += 2
		case 't':
			r.word = append(r.word, '\t')
			r.pos += 2
		case 'r':
			r.word = append(r.word, '\r')
			r.pos += 2
		case '\\', '\'', '"':
			r.word = append(r.word, n)
			r.pos += 2
		case 'x':
			j := r.pos + 2
			val, digits := 0, 0
			for j < len(r.src) && digits < 2 {
				d, ok := hexDigit(r.src[j])
				if !ok {
					break
				}
				val = val*16 + d
				j++
				digits++
			}
			if digits == 0 {
				r.word = append(r.word, '\\', 'x')
				r.pos += 2
				continue
			}
			r.word = append(r.word, byte(val))
			r.pos = j
		default:
			r.word = append(r.word, '\\', n)
			r.pos += 2
		}
	}
	r.opaque = append(r.opaque, "unterminated $' quote")
}

// readDollar handles $VAR, ${VAR}, $(...) and the expansion a rule can evaluate:
// ${IFS} and $IFS end the word outside quotes, because the shell splits on them.
func (r *shellReader) readDollar() {
	rest := r.src[r.pos:]
	if strings.HasPrefix(rest, "$(") {
		r.pos += 2
		r.readSubstitution()
		return
	}
	if strings.HasPrefix(rest, "${") {
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			r.opaque = append(r.opaque, "unterminated ${")
			r.pos = len(r.src)
			return
		}
		name := rest[2:end]
		r.pos += end + 1
		r.wordExpansion(name)
		return
	}
	j := r.pos + 1
	for j < len(r.src) && isShellNameChar(r.src[j], j == r.pos+1) {
		j++
	}
	if j == r.pos+1 {
		r.word = append(r.word, '$')
		r.pos++
		return
	}
	name := r.src[r.pos+1 : j]
	r.pos = j
	r.wordExpansion(name)
}

// wordExpansion contributes one variable expansion to the word being read.
func (r *shellReader) wordExpansion(name string) {
	r.inWord = true
	if name == "IFS" && !r.inDouble {
		r.word = append(r.word, ifsBreak...)
		return
	}
	r.segDyn = true
	r.word = append(r.word, dynamicWord...)
}

func (r *shellReader) readBacktick() {
	end := strings.IndexByte(r.src[r.pos+1:], '`')
	if end < 0 {
		r.opaque = append(r.opaque, "unterminated backtick")
		r.pos = len(r.src)
		return
	}
	inner := r.src[r.pos+1 : r.pos+1+end]
	r.pos += end + 2
	r.nested(inner)
	r.word = append(r.word, dynamicWord...)
	r.inWord = true
	r.segDyn = true
}

func (r *shellReader) readSubstitution() {
	depth := 1
	start := r.pos
	for r.pos < len(r.src) {
		switch r.src[r.pos] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				inner := r.src[start:r.pos]
				r.pos++
				r.nested(inner)
				r.word = append(r.word, dynamicWord...)
				r.inWord = true
				r.segDyn = true
				return
			}
		case '\'':
			if end := strings.IndexByte(r.src[r.pos+1:], '\''); end >= 0 {
				r.pos += end + 2
				continue
			}
		}
		r.pos++
	}
	r.opaque = append(r.opaque, "unterminated $(")
}

// nested folds a substituted command into the same segment list, so a rule sees
// the command inside the command.
func (r *shellReader) nested(src string) {
	inner := ParseCommand(src)
	r.segments = append(r.segments, inner.Segments...)
	r.opaque = append(r.opaque, inner.Opaque...)
}

func (r *shellReader) readOperator() {
	rest := r.src[r.pos:]
	switch {
	case strings.HasPrefix(rest, "&&"), strings.HasPrefix(rest, "||"):
		r.pos += 2
	case strings.HasPrefix(rest, "|&"):
		r.pos += 2
	default:
		r.pos++
	}
	r.endSegment()
}

func (r *shellReader) readRedirect() {
	// A bare number immediately before the operator is a file descriptor, not an
	// argument: `ls 2>err` writes stderr to err and passes ls no "2".
	if r.inWord && isAllDigits(string(r.word)) {
		r.word = r.word[:0]
		r.inWord = false
	} else {
		r.endWord()
	}

	rest := r.src[r.pos:]
	op := ""
	switch {
	case strings.HasPrefix(rest, "<<<"):
		op = "<<<"
	case strings.HasPrefix(rest, "&>>"):
		op = "&>>"
	case strings.HasPrefix(rest, "&>"):
		op = "&>"
	case strings.HasPrefix(rest, ">>"):
		op = ">>"
	case strings.HasPrefix(rest, ">|"):
		op = ">|"
	case strings.HasPrefix(rest, "<<"):
		op = "<<"
	case strings.HasPrefix(rest, "<>"):
		op = "<>"
	case strings.HasPrefix(rest, "<&"), strings.HasPrefix(rest, ">&"):
		op = rest[:1] + "&"
	case rest[0] == '>':
		op = ">"
	default:
		op = "<"
	}
	r.pos += len(op)

	target := r.readTargetWord()
	switch op {
	case ">", ">>", ">|", "&>", "&>>", "<>":
		if target == "" {
			r.opaque = append(r.opaque, "redirection with no target")
			return
		}
		if strings.Contains(target, dynamicWord) {
			r.opaque = append(r.opaque, "redirection to a path computed at run time")
			return
		}
		if isNullSink(target) {
			return
		}
		r.outputs = append(r.outputs, target)
	case "<<", "<<<":
		r.heredoc = true
		r.consumeHeredocBody(target, op == "<<<")
	}
}

// readTargetWord reads the word after a redirect operator.
func (r *shellReader) readTargetWord() string {
	for r.pos < len(r.src) && (r.src[r.pos] == ' ' || r.src[r.pos] == '\t') {
		r.pos++
	}
	var out []byte
	for r.pos < len(r.src) {
		c := r.src[r.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '&' || c == '|':
			r.pos++
			return string(out)
		case c == '\'':
			end := strings.IndexByte(r.src[r.pos+1:], '\'')
			if end < 0 {
				r.opaque = append(r.opaque, "unterminated quote in redirection")
				r.pos = len(r.src)
				return string(out)
			}
			out = append(out, r.src[r.pos+1:r.pos+1+end]...)
			r.pos += end + 2
		case c == '"':
			r.pos++
			for r.pos < len(r.src) && r.src[r.pos] != '"' {
				if r.src[r.pos] == '$' {
					out = append(out, dynamicWord...)
				} else {
					out = append(out, r.src[r.pos])
				}
				r.pos++
			}
			r.pos++
		case c == '$':
			r.pos++
			out = append(out, dynamicWord...)
		default:
			out = append(out, c)
			r.pos++
		}
	}
	return string(out)
}

// consumeHeredocBody skips the body up to its delimiter, so the body is not read
// as a sequence of commands.
func (r *shellReader) consumeHeredocBody(delimiter string, hereString bool) {
	if hereString {
		for r.pos < len(r.src) && r.src[r.pos] != '\n' {
			r.pos++
		}
		return
	}
	delim := strings.Trim(delimiter, "'\"")
	if delim == "" {
		return
	}
	for r.pos < len(r.src) && r.src[r.pos] != '\n' {
		r.pos++
	}
	for r.pos < len(r.src) {
		lineStart := r.pos + 1
		if lineStart >= len(r.src) {
			return
		}
		lineEnd := strings.IndexByte(r.src[lineStart:], '\n')
		var line string
		if lineEnd < 0 {
			line = r.src[lineStart:]
			r.pos = len(r.src)
		} else {
			line = r.src[lineStart : lineStart+lineEnd]
			r.pos = lineStart + lineEnd
		}
		if strings.TrimSpace(line) == delim {
			return
		}
		if lineEnd < 0 {
			return
		}
	}
}

// endWord finishes the word being read, splitting it wherever an unquoted ${IFS}
// expanded to a field separator.
func (r *shellReader) endWord() {
	if !r.inWord {
		return
	}
	for _, part := range strings.Split(string(r.word), ifsBreak) {
		if part == "" {
			continue
		}
		r.argv = append(r.argv, part)
	}
	r.word = r.word[:0]
	r.inWord = false
}

// endSegment closes the current simple command, if there is one.
func (r *shellReader) endSegment() {
	r.endWord()
	if len(r.argv) == 0 && len(r.outputs) == 0 && !r.heredoc {
		r.segDyn = false
		return
	}
	argv := r.argv
	// A leading assignment is the environment of the command, not its program.
	for len(argv) > 1 && isAssignment(argv[0]) {
		argv = argv[1:]
	}
	seg := ShellSegment{
		Argv:    argv,
		Outputs: r.outputs,
		Heredoc: r.heredoc,
		Dynamic: r.segDyn,
	}
	if len(seg.Argv) > 0 && strings.Contains(seg.Argv[0], dynamicWord) {
		seg.Dynamic = true
	}
	r.segments = append(r.segments, r.unwrap(seg)...)
	r.argv = nil
	r.outputs = nil
	r.heredoc = false
	r.segDyn = false
}

// unwrap expands the wrapper programs that run another program, so a rule sees
// the program that will actually run.
func (r *shellReader) unwrap(seg ShellSegment) []ShellSegment {
	if len(seg.Argv) == 0 {
		return []ShellSegment{seg}
	}
	prog := baseProgram(seg.Argv[0])
	args := seg.Argv[1:]
	more := func(chain string, argv []string) []ShellSegment {
		inner := ShellSegment{
			Argv:    argv,
			Outputs: seg.Outputs,
			Heredoc: seg.Heredoc,
			Dynamic: seg.Dynamic,
			Via:     append(append([]string{}, seg.Via...), chain),
		}
		return r.unwrap(inner)
	}
	switch prog {
	case "sh", "bash", "zsh", "dash", "ksh":
		for i := 0; i < len(args); i++ {
			a := args[i]
			if a == "--" {
				break
			}
			if strings.HasPrefix(a, "-") && len(a) > 1 {
				// -c, and the combined forms that carry it (-lc, -ec) where the
				// command string is the next argument.
				if strings.ContainsRune(a[1:], 'c') && i+1 < len(args) {
					inner := ParseCommand(args[i+1])
					var out []ShellSegment
					for _, s := range inner.Segments {
						s.Via = append(append([]string{}, seg.Via...), prog, "-c")
						out = append(out, s)
					}
					r.opaque = append(r.opaque, inner.Opaque...)
					return out
				}
				continue
			}
			break
		}
		return []ShellSegment{seg}
	case "!":
		if rest := skipOptions(args); len(rest) > 0 {
			return more("!", rest)
		}
		return []ShellSegment{seg}
	case "env":
		if rest := skipEnvPrefix(args); len(rest) > 0 {
			return more("env", rest)
		}
		return []ShellSegment{seg}
	case "command", "exec", "nohup", "time":
		if rest := skipOptions(args); len(rest) > 0 {
			return more(prog, rest)
		}
		return []ShellSegment{seg}
	case "nice":
		if rest := skipValues(args, map[string]bool{"-n": true, "--adjustment": true}); len(rest) > 0 {
			return more("nice", rest)
		}
		return []ShellSegment{seg}
	case "stdbuf":
		if rest := skipOptions(args); len(rest) > 0 {
			return more("stdbuf", rest)
		}
		return []ShellSegment{seg}
	case "timeout":
		if rest := skipTimeout(args); len(rest) > 0 {
			return more("timeout", rest)
		}
		return []ShellSegment{seg}
	case "xargs":
		if rest := skipXargsOptions(args); len(rest) > 0 {
			return more("xargs", rest)
		}
		return []ShellSegment{seg}
	case "eval":
		r.opaque = append(r.opaque, "eval builds a command at run time")
		return []ShellSegment{seg}
	case "source", ".":
		r.opaque = append(r.opaque, "sources a file the reader cannot see")
		return []ShellSegment{seg}
	}
	return []ShellSegment{seg}
}

// skipEnvPrefix drops env's own options and VAR=value assignments.
func skipEnvPrefix(args []string) []string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-i" || a == "--ignore-environment":
			continue
		case a == "-u" || a == "--unset" || a == "-C" || a == "--chdir":
			i++
		case strings.HasPrefix(a, "--unset="), strings.HasPrefix(a, "--chdir="):
			continue
		case strings.HasPrefix(a, "-"):
			continue
		case strings.Contains(a, "=") && !strings.HasPrefix(a, "="):
			continue
		default:
			return args[i:]
		}
	}
	return nil
}

// skipValues drops single-dash options, including the ones named in withValue,
// and stops at the first operand.
func skipValues(args []string, withValue map[string]bool) []string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return args[i:]
		}
		if withValue[a] {
			i++
		}
	}
	return nil
}

// skipOptions is skipValues with no option taking a separate argument.
func skipOptions(args []string) []string { return skipValues(args, nil) }

// skipTimeout drops timeout's duration and its options.
func skipTimeout(args []string) []string {
	dur := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-s" || a == "--signal" || a == "-k" || a == "--kill-after":
			i++
		case strings.HasPrefix(a, "-"):
			continue
		case !dur:
			dur = true
		default:
			return args[i:]
		}
	}
	return nil
}

// skipXargsOptions drops xargs options, including the ones that take a value.
func skipXargsOptions(args []string) []string {
	withValue := map[string]bool{"-I": true, "-n": true, "-P": true, "-d": true, "-L": true, "-s": true, "-E": true, "-a": true}
	return skipValues(args, withValue)
}

// baseProgram returns the name a rule matches on: the last path element, so
// /bin/rm reads as rm.
func baseProgram(arg string) string {
	if strings.Contains(arg, dynamicWord) {
		return arg
	}
	return filepath.Base(arg)
}

func hexDigit(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}

func isShellNameChar(c byte, first bool) bool {
	if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' {
		return true
	}
	return !first && c >= '0' && c <= '9'
}

// isAssignment reports whether a word is a leading NAME=value assignment.
func isAssignment(word string) bool {
	name, _, ok := strings.Cut(word, "=")
	if !ok || name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isShellNameChar(name[i], i == 0) {
			return false
		}
	}
	return true
}

// isAllDigits reports whether a word is a bare file-descriptor number.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isNullSink reports whether a redirect target is a sink rather than a file.
func isNullSink(target string) bool {
	switch target {
	case "/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty":
		return true
	}
	return false
}
