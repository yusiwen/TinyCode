package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file covers the non-Go language-server path of issue #10. Most of it needs
// no real server: a shim on PATH re-executes *this test binary* as a stand-in
// language server, which is enough to prove the selection, the argument list, the
// negotiated languageId, the document URI and the diagnostics round trip for a
// TypeScript file. That runs in the ungated `ci` job on every push. A gated case
// then runs the same round trip against the real typescript-language-server when
// it is installed, and skips with the reason when it is not.

// TestFakeLanguageServerProcess is not a test. Re-executed with
// TINYCODE_FAKE_LSP=1 it serves the protocol on stdin/stdout until the client
// closes the stream, then exits — through os.Exit, so the testing framework never
// prints anything on stdout that could be mistaken for a frame.
func TestFakeLanguageServerProcess(t *testing.T) {
	if os.Getenv("TINYCODE_FAKE_LSP") != "1" {
		t.Skip("helper process: the stand-in language server runs with TINYCODE_FAKE_LSP=1")
	}

	logPath := os.Getenv("TINYCODE_FAKE_LSP_LOG")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake server: open log: %v\n", err)
		os.Exit(1)
	}
	fakeServerLoop(os.Stdin, os.Stdout, logFile)
	logFile.Close()
	os.Exit(0)
}

// fakeServerLoop answers the handshake, records every document sync and pushes one
// error-severity diagnostic for it — the smallest server the client can complete a
// round trip against. `logw` receives `method=… uri=… languageId=…` lines, which is
// what the tests assert on.
func fakeServerLoop(r io.Reader, w io.Writer, logw io.Writer) {
	reader := bufio.NewReader(r)
	for {
		body, err := readFakeFrame(reader)
		if err != nil {
			return
		}
		var msg struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(body, &msg) != nil {
			continue
		}

		switch msg.Method {
		case "initialize":
			writeFakeFrame(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":{"capabilities":{}}}`, msg.ID))
		case "initialized", "workspace/didChangeConfiguration", "$/setTrace":
			// Notifications: nothing to answer.
		case "textDocument/didOpen", "textDocument/didChange":
			var p struct {
				TextDocument struct {
					URI        string `json:"uri"`
					LanguageID string `json:"languageId"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			fmt.Fprintf(logw, "method=%s uri=%s languageId=%s\n", msg.Method, p.TextDocument.URI, p.TextDocument.LanguageID)
			if f, ok := logw.(*os.File); ok {
				_ = f.Sync()
			}
			writeFakeFrame(w, fmt.Sprintf(
				`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{`+
					`"uri":%q,"diagnostics":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}},`+
					`"severity":1,"message":"stand-in server diagnostic","source":"fake"}]}}`,
				p.TextDocument.URI))
		case "shutdown":
			writeFakeFrame(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":null}`, msg.ID))
		case "exit":
			return
		default:
			if msg.ID != nil {
				writeFakeFrame(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":null}`, msg.ID))
			}
		}
	}
}

// readFakeFrame reads one Content-Length framed message.
func readFakeFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			if n, convErr := strconv.Atoi(strings.TrimSpace(value)); convErr == nil {
				length = n
			}
		}
	}
	if length < 0 {
		return nil, errors.New("frame without Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func writeFakeFrame(w io.Writer, body string) {
	fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

// installFakeServer puts an executable named `name` on PATH that appends its
// arguments to logPath and then re-executes this test binary as the stand-in
// server. It returns the directory to put in front of PATH.
func installFakeServer(t *testing.T, name, logPath string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	dir := t.TempDir()
	script := fmt.Sprintf(
		"#!/bin/sh\nprintf 'args=%%s\\n' \"$*\" >> %s\nTINYCODE_FAKE_LSP=1 TINYCODE_FAKE_LSP_LOG=%s exec %s -test.run=TestFakeLanguageServerProcess\n",
		shellQuotePath(logPath), shellQuotePath(logPath), shellQuotePath(self))
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatalf("write the shim: %v", err)
	}
	return dir
}

// shellQuotePath wraps a path in single quotes for /bin/sh.
func shellQuotePath(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// readFakeLog waits for the stand-in server to have written at least one line and
// returns the whole log. The server runs in another process, so the file appears
// asynchronously.
func readFakeLog(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return string(data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("the stand-in server never wrote to %s (log: %q)", path, data)
	return ""
}

// TestSyncFileUsesTheTypeScriptServer covers the non-Go path end to end against
// the stand-in server: the language that was selected, the arguments it was
// started with, the languageId and URI it was told about, and the diagnostic that
// came back (issue #10).
func TestSyncFileUsesTheTypeScriptServer(t *testing.T) {
	proj := t.TempDir()
	app := filepath.Join(proj, "app.ts")
	content := "const answer: number = 42\n"
	if err := os.WriteFile(app, []byte(content), 0644); err != nil {
		t.Fatalf("write app.ts: %v", err)
	}

	logPath := filepath.Join(t.TempDir(), "server.log")
	t.Setenv("PATH", installFakeServer(t, "typescript-language-server", logPath))

	Init(proj)
	defer Init("")

	diags, err := SyncFile(app, content, true)
	if err != nil {
		t.Fatalf("SyncFile: %v", err)
	}
	if !IsAvailable() {
		t.Fatal("the TypeScript server was not started")
	}

	// The diagnostic the stand-in pushed proves the whole notification path:
	// didOpen → server → publishDiagnostics → client.
	if len(diags) == 0 || diags[0].Severity != 1 || !strings.Contains(diags[0].Message, "stand-in") {
		t.Errorf("diagnostics = %+v, want the stand-in's error-severity diagnostic", diags)
	}

	log := readFakeLog(t, logPath)
	if !strings.Contains(log, "args=--stdio") {
		t.Errorf("the server was not started with --stdio:\n%s", log)
	}
	if !strings.Contains(log, "languageId=typescript") {
		t.Errorf("the negotiated languageId is not typescript:\n%s", log)
	}
	if want := "uri=file://" + canonicalPath(app); !strings.Contains(log, want) {
		t.Errorf("the server was not told about %s:\n%s", want, log)
	}
}

// TestSyncFileTypeScriptWithoutServerNamesTheServer covers the missing-binary path
// for a non-Go project: the failure must name the TypeScript server, and it must
// not leave the package claiming a working server.
func TestSyncFileTypeScriptWithoutServerNamesTheServer(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing resolvable

	proj := t.TempDir()
	app := filepath.Join(proj, "app.ts")
	if err := os.WriteFile(app, []byte("const x = 1\n"), 0644); err != nil {
		t.Fatalf("write app.ts: %v", err)
	}

	Init(proj)
	defer Init("")

	_, err := SyncFile(app, "const x = 1\n", true)
	if err == nil {
		t.Fatal("a project whose server is not installed must fail")
	}
	if !strings.Contains(err.Error(), "typescript-language-server") {
		t.Errorf("error = %v, want it to name typescript-language-server", err)
	}
	if IsAvailable() {
		t.Error("LSP reports itself available after a failed start")
	}
}

// TestSyncFileTypeScriptRealServer runs the same round trip against the real
// typescript-language-server when it is installed, and skips with the reason when
// it is not: the CI `lsp` job installs it, a developer machine often has not.
func TestSyncFileTypeScriptRealServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping LSP integration test in short mode")
	}
	if os.Getenv("LSP_TEST") == "" {
		t.Skip("skipping: set LSP_TEST=1 to run LSP integration tests")
	}
	bin, err := exec.LookPath("typescript-language-server")
	if err != nil {
		t.Skipf("typescript-language-server is not installed: %v", err)
	}

	proj := t.TempDir()
	app := filepath.Join(proj, "broken.ts")
	// A syntax error, so the answer does not depend on type inference, plus the
	// project shape tsserver expects: without a tsconfig it treats the file as an
	// inferred project and, on this version, publishes nothing for it.
	if err := os.WriteFile(app, []byte("const answer: number = ;\n"), 0644); err != nil {
		t.Fatalf("write broken.ts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "tsconfig.json"),
		[]byte("{\"compilerOptions\":{\"strict\":true},\"include\":[\"*.ts\"]}\n"), 0644); err != nil {
		t.Fatalf("write tsconfig.json: %v", err)
	}

	// tsserver resolves its `typescript` package from the workspace, so a real
	// project has it in node_modules (the server refuses to start without one:
	// "Could not find a valid TypeScript installation"). Link the installation
	// that sits next to the language server, which is where a global npm install
	// puts it, instead of running a package manager in a test.
	tsDir := filepath.Join(filepath.Dir(bin), "..", "lib", "node_modules", "typescript")
	if _, err := os.Stat(filepath.Join(tsDir, "lib", "tsserver.js")); err != nil {
		t.Skipf("no TypeScript installation next to %s: %v", bin, err)
	}
	if err := os.MkdirAll(filepath.Join(proj, "node_modules"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tsDir, filepath.Join(proj, "node_modules", "typescript")); err != nil {
		t.Fatal(err)
	}

	Init(proj)
	defer Init("")

	// tsserver starts slower than gopls and answers after it has loaded the
	// project, so this case waits longer than the Go ones.
	waitForErrorsWithin(t, app, true, 30*time.Second)
}

// TestInitializeAdvertisesDiagnosticsSupport pins the capability that issue #10
// found the hard way: typescript-language-server publishes no diagnostics at all
// while `textDocument.publishDiagnostics` is missing from the client capabilities,
// so a regression here turns every non-Go file silently clean. This runs ungated —
// the real server that revealed it needs an npm install.
func TestInitializeAdvertisesDiagnosticsSupport(t *testing.T) {
	mock, conn := newMockLSP()
	defer mock.close()

	if err := NewClient(conn).Initialize("file:///workspace"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	params := mock.initializeParams()
	if params == "" {
		t.Fatal("the server never received initialize")
	}
	if !strings.Contains(params, `"publishDiagnostics"`) {
		t.Errorf("capabilities do not advertise publishDiagnostics support:\n%s", params)
	}
	if !strings.Contains(params, `"workspaceFolders"`) {
		t.Errorf("initialize does not root the workspace:\n%s", params)
	}
}
