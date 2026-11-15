package tool

import (
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// TestCheckCommandDeniesWhatTheTextHid is the reason the deny list reads the
// command: every one of these got past a substring match, because the text never
// contains the pattern that was being looked for.
func TestCheckCommandDeniesWhatTheTextHid(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"quoted program", `r"m" -rf /`},
		{"ifs expansion", `rm${IFS}-rf /`},
		{"ansi-c quoting", `$'\x72\x6d' -rf /`},
		{"shell -c", `sh -c 'rm -rf /'`},
		{"xargs", `xargs rm -rf /`},
		{"env prefix", `env -i rm -rf /`},
		{"assignment prefix", `FOO=1 rm -rf /`},
		{"device format", `mkfs.ext4 /dev/sda1`},
		{"raw device", `dd if=/dev/zero of=/dev/sda`},
		{"privilege escalation", `sudo -u root cat /etc/shadow`},
		{"power", `systemctl reboot`},
		{"recursive chmod on root", `chmod -R 777 /`},
		{"chain", `ls && rm -rf /`},
		{"substitution", `echo $(rm -rf /)`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := DefaultSandbox.CheckCommand(c.cmd); err == nil {
				t.Errorf("CheckCommand(%q) allowed a command a rule names", c.cmd)
			}
		})
	}
}

// TestCheckCommandAdmitsWordsInArguments is the other half: the old match refused
// the word wherever it appeared, including inside a quoted argument that runs
// nothing.
func TestCheckCommandAdmitsWordsInArguments(t *testing.T) {
	cases := []string{
		`echo "sudo "`,
		`git commit -m "chmod -R /"`,
		`grep -rn "mkfs" .`,
		`rm -rf /tmp/scratch`,
		`dd if=/dev/zero of=./image.bin`,
		`chmod -R 755 ./dir`,
		`ls -la`,
	}
	for _, cmd := range cases {
		if err := DefaultSandbox.CheckCommand(cmd); err != nil {
			t.Errorf("CheckCommand(%q) refused a harmless command: %v", cmd, err)
		}
	}
}

// TestCheckCommandReadsConfiguredEntriesAsArgvPrefixes pins the contract of
// sandbox.deny_commands: an entry names a program (and optionally its first
// arguments), not a substring.
func TestCheckCommandReadsConfiguredEntriesAsArgvPrefixes(t *testing.T) {
	sc := &SandboxConfig{DenyCommands: []string{"curl", "npm publish"}}
	for _, cmd := range []string{`curl https://example.com`, `env -i curl https://example.com`, `/usr/bin/curl -s x`} {
		if err := sc.CheckCommand(cmd); err == nil {
			t.Errorf("CheckCommand(%q) did not apply the curl entry", cmd)
		}
	}
	if err := sc.CheckCommand(`npm publish --access public`); err == nil {
		t.Error("the npm publish entry did not apply")
	}
	for _, cmd := range []string{`echo curl`, `git log --grep curl`, `npm install`} {
		if err := sc.CheckCommand(cmd); err != nil {
			t.Errorf("CheckCommand(%q) matched a word in an argument: %v", cmd, err)
		}
	}
}

// TestCheckCommandIgnoresAnEmptyEntry is a regression guard: an empty pattern used
// to block every command, which turned a stray comma in a config file into
// "nothing runs".
func TestCheckCommandIgnoresAnEmptyEntry(t *testing.T) {
	sc := &SandboxConfig{DenyCommands: []string{"", "  "}}
	if err := sc.CheckCommand("ls"); err != nil {
		t.Errorf("an empty deny entry blocked a command: %v", err)
	}
}

// TestCheckCommandFallsBackToWordsWhenItCannotRead covers the opaque path: when
// the reader gives up, the rules still look for their program names as words, so
// a construct it could not classify cannot clear a denial by hiding it.
func TestCheckCommandFallsBackToWordsWhenItCannotRead(t *testing.T) {
	if err := DefaultSandbox.CheckCommand(`eval "sudo shutdown -h now"`); err == nil {
		t.Error("an opaque command mentioning a denied program was allowed")
	}
	// The fallback matches words, not substrings: a mention inside a longer word
	// is not a program.
	if err := DefaultSandbox.CheckCommand(`eval "x-sudo-shutdown-helper"`); err != nil {
		t.Errorf("the opaque fallback matched inside a word: %v", err)
	}
}

// TestReadOnlyBoundaryNamesWhatIsMissing pins the three answers a read-only run
// can get: a boundary, a switch that turns it off, and a host that has none.
func TestReadOnlyBoundaryNamesWhatIsMissing(t *testing.T) {
	previousConfine := ConfineCommands()
	previousProbe := setCommandConfinementProbeForTest(func() bool { return true })
	t.Cleanup(func() {
		SetConfineCommands(previousConfine)
		previousProbe()
	})

	SetConfineCommands(true)
	if ok, missing := readOnlyBoundary(types.SandboxPolicy{Mode: types.SandboxReadOnly}); !ok {
		t.Errorf("a confined host has no boundary: %s", missing)
	}

	SetConfineCommands(false)
	ok, missing := readOnlyBoundary(types.SandboxPolicy{Mode: types.SandboxReadOnly})
	if ok || !strings.Contains(missing, "confine_commands") {
		t.Errorf("a host with confinement off reported ok=%v missing=%q", ok, missing)
	}

	SetConfineCommands(true)
	previousProbe()
	previousProbe = setCommandConfinementProbeForTest(func() bool { return false })
	ok, missing = readOnlyBoundary(types.SandboxPolicy{Mode: types.SandboxReadOnly})
	if ok || !strings.Contains(missing, "no kernel file boundary") {
		t.Errorf("a host without a mechanism reported ok=%v missing=%q", ok, missing)
	}

	// A container-tier read-only run is a boundary of its own: the invocation
	// probes the runtime and fails closed.
	if ok, missing := readOnlyBoundary(types.SandboxPolicy{Mode: types.SandboxReadOnly, Tier: types.SandboxTierContainer}); !ok {
		t.Errorf("the container tier was not accepted as a boundary: %s", missing)
	}
}

// TestReadOnlyWriteHintNamesTheWrite is the courtesy refusal: it names the file
// or the program, and it stays quiet about what it cannot read.
func TestReadOnlyWriteHintNamesTheWrite(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{`mkdir x`, "mkdir"},
		{`echo hi > out.txt`, "out.txt"},
		{`sh -c 'mkdir x'`, "sh -c"},
		{`cp a b`, "cp"},
		{`ls -la`, ""},
		{`echo hi > /dev/null`, ""},
		{`python3 -c "open('x','w')"`, ""},
		{`git status`, ""},
	}
	for _, c := range cases {
		got := readOnlyWriteHint(c.cmd)
		if c.want == "" {
			if got != "" {
				t.Errorf("readOnlyWriteHint(%q) = %q, want no hint", c.cmd, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("readOnlyWriteHint(%q) = %q, want it to name %q", c.cmd, got, c.want)
		}
	}
}

// TestIsRootTargetReadsBothConventions is the platform-neutral half of the
// destructive-rm rule. `rm -rf /` must be caught on Windows too, where
// filepath.Clean turns "/" into "\\", and the Windows spelling of a root ("C:/")
// has to be caught on every platform the command text can reach.
func TestIsRootTargetReadsBothConventions(t *testing.T) {
	roots := []string{`/`, `/*`, `/**`, `/tmp/..`, `C:/`, `c:\`, `//`}
	for _, target := range roots {
		if !isRootTarget(target) {
			t.Errorf("isRootTarget(%q) = false, want true", target)
		}
	}
	notRoots := []string{``, `/tmp`, `./x`, `C:/tmp`, `..`, `internal/store.go`}
	for _, target := range notRoots {
		if isRootTarget(target) {
			t.Errorf("isRootTarget(%q) = true, want false", target)
		}
	}
	if err := DefaultSandbox.CheckCommand(`r"m" -rf C:/`); err == nil {
		t.Error("a Windows root was not denied")
	}
}
