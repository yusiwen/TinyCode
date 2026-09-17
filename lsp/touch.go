package lsp

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/yusiwen/tinycode/tlog"
)

var (
	mu            sync.Mutex
	lspAvailable  bool
	client        *Client
	conn          *Conn
	projectRoot   string
	serverCmd     *exec.Cmd               // persistent server process (lazyStart)
	diagBaselines map[string][]Diagnostic // path → pre-write diagnostics
)

// Init initializes the LSP system. Call once at startup if LSP is enabled.
// Init sets the workspace root. If a server is already running for a different
// root it is shut down, because a language server is bound to the workspace it
// was started with: leaving it alive made files of the new project report "not
// included in your workspace" instead of real diagnostics.
func Init(root string) {
	mu.Lock()
	defer mu.Unlock()

	if client != nil && canonicalPath(projectRoot) != canonicalPath(root) {
		shutdownLocked()
	}
	projectRoot = root
	// LSP server is started lazily on first TouchFile call
	// Drop diagnostics recorded for a previous workspace.
	resetDiagnostics()
}

// shutdownLocked stops the persistent server and clears the session. The caller
// holds mu.
func shutdownLocked() {
	if client != nil {
		_ = client.Shutdown() // shutdown request + exit notification
	}
	if conn != nil {
		_ = conn.Close()
	}
	client = nil
	conn = nil
	lspAvailable = false

	if serverCmd != nil {
		// Reap the child so it does not linger as a zombie; kill it if it does
		// not exit on its own.
		done := make(chan struct{})
		cmd := serverCmd
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		serverCmd = nil
	}
}

// IsAvailable returns true if LSP is initialized and not broken.
func IsAvailable() bool {
	mu.Lock()
	defer mu.Unlock()
	return lspAvailable && client != nil
}

// TouchFile opens a file in the LSP server.
// If withDiagnostics is true, waits up to 5 seconds for diagnostics.
// Returns diagnostics if any, or nil on timeout/failure.
// canonicalPath returns the OS-resolved absolute form of path.
//
// Language servers canonicalize the workspace root themselves, so sending a
// document URI built from an unresolved path (macOS /tmp is a symlink to
// /private/tmp) makes the server treat the document as outside the workspace and
// answer with "not included in your workspace" instead of real diagnostics.
func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	// The file may not exist yet: resolve the deepest existing ancestor.
	dir, base := filepath.Split(abs)
	if resolvedDir, err := filepath.EvalSymlinks(filepath.Clean(dir)); err == nil {
		return filepath.Join(resolvedDir, base)
	}
	return abs
}

func TouchFile(filePath string, withDiagnostics bool) ([]Diagnostic, error) {
	mu.Lock()
	// Lazy start: spawn the language server on first use
	if client == nil {
		if err := lazyStart(filePath); err != nil {
			mu.Unlock()
			return nil, err
		}
	}
	mu.Unlock()

	// Build the file URI from the canonical path so it matches the workspace
	// root the server resolves to.
	absPath := canonicalPath(filePath)
	uri := "file://" + absPath

	// Read the file once: the server must see the real text, otherwise it
	// analyses an empty buffer and reports phantom errors (or none at all).
	data, readErr := os.ReadFile(absPath)
	if readErr != nil {
		return nil, readErr
	}
	content := string(data)

	if !withDiagnostics {
		// Fire-and-forget: sync the document, no waiting
		tlog.Debug("lsp.touch", "warmup", "file", absPath)
		if err := client.SyncDocument(uri, content); err != nil {
			log.Printf("LSP warmup: notify open: %v", err)
		}
		return nil, nil
	}

	// With diagnostics: sync and wait
	tlog.Debug("lsp.touch", "diagnostics", "file", absPath)
	diags, err := client.Diagnostics(uri, content)
	if err != nil {
		log.Printf("LSP diagnostics: %v", err)
		return nil, nil // silent failure
	}
	tlog.Debug("lsp.touch", "diag_result", "file", absPath, "count", len(diags))
	return diags, nil
}

// SnapshotBaseline captures the current diagnostics for a file before editing.
// Call before write_file to establish a baseline for delta diagnostics.
func SnapshotBaseline(path string) {
	diags, err := TouchFile(path, true)
	if err != nil {
		tlog.Debug("lsp.baseline", "snapshot_error", "file", path, "error", err.Error())
		return
	}
	mu.Lock()
	if diagBaselines == nil {
		diagBaselines = make(map[string][]Diagnostic)
	}
	diagBaselines[path] = diags
	mu.Unlock()
	tlog.Debug("lsp.baseline", "snapshot", "file", path, "count", len(diags))
}

// GetNewDiagnostics compares current diagnostics against the baseline.
// Returns only diagnostics not in the baseline snapshot.
// Call after write_file to get only the errors introduced by the edit.
func GetNewDiagnostics(path string) []Diagnostic {
	current, err := TouchFile(path, true)
	if err != nil || len(current) == 0 {
		return nil
	}

	mu.Lock()
	baseline := diagBaselines[path]
	mu.Unlock()

	if len(baseline) == 0 {
		return current
	}

	// Build a set of baseline diagnostic signatures (line:message)
	type sig struct {
		line, col int
		msg       string
	}
	baselineSet := make(map[sig]bool, len(baseline))
	for _, d := range baseline {
		baselineSet[sig{line: d.Range.Start.Line, col: d.Range.Start.Character, msg: d.Message}] = true
	}

	// Return diagnostics not in the baseline
	var newDiags []Diagnostic
	for _, d := range current {
		if !baselineSet[sig{line: d.Range.Start.Line, col: d.Range.Start.Character, msg: d.Message}] {
			newDiags = append(newDiags, d)
		}
	}

	tlog.Debug("lsp.baseline", "delta", "file", path, "baseline", len(baseline), "current", len(current), "new", len(newDiags))
	return newDiags
}

// lazyStart starts the LSP server for filePath's project language, falling back
// to the file's own extension when the project cannot be identified.
func lazyStart(filePath string) error {
	lang := serverLanguage(projectRoot, filePath)
	tlog.Info("lsp.touch", "lazy_start", "root", projectRoot, "file", filePath, "language", lang)
	if lang == "" {
		lspAvailable = false
		return fmt.Errorf("no LSP server configured for %s", filePath)
	}
	cfg := FindConfig(lang)
	if cfg == nil {
		lspAvailable = false
		return fmt.Errorf("no LSP server configured for %s", lang)
	}
	rootDir := canonicalPath(projectRoot)
	cmd := exec.Command(cfg.Command, cfg.Args...)
	// Run the server inside the project: a server that falls back to its working
	// directory as the workspace would otherwise analyse the wrong tree.
	if rootDir != "" {
		cmd.Dir = rootDir
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		tlog.Warn("lsp.touch", "stdin_pipe_failed", "error", err.Error())
		log.Printf("LSP start: stdin pipe: %v", err)
		lspAvailable = false
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("LSP start: stdout pipe: %v", err)
		lspAvailable = false
		return err
	}

	if err := cmd.Start(); err != nil {
		log.Printf("LSP start: %s not found: %v", cfg.Command, err)
		lspAvailable = false
		return err
	}

	conn = NewConn(stdin, stdout)
	serverCmd = cmd
	c := NewClient(conn)
	rootURI := "file://" + rootDir
	if err := c.Initialize(rootURI); err != nil {
		log.Printf("LSP init: %v", err)
		cmd.Process.Kill()
		lspAvailable = false
		return err
	}

	client = c
	lspAvailable = true
	log.Printf("LSP: %s started for %s", cfg.Command, rootDir)
	return nil
}

// (Document open/change notifications live in Client.SyncDocument, which sends
// the real text and switches to didChange once a document is open.)
