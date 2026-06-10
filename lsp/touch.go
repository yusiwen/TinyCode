package lsp

import (
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/yusiwen/tinycode/tlog"
)

var (
	mu           sync.Mutex
	lspAvailable bool
	server       *Server
	client       *Client
	conn         *Conn
	projectRoot  string
	diagBaselines map[string][]Diagnostic // path → pre-write diagnostics
)

// Init initializes the LSP system. Call once at startup if LSP is enabled.
func Init(root string) {
	mu.Lock()
	defer mu.Unlock()
	projectRoot = root
	// LSP server is started lazily on first TouchFile call
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
func TouchFile(filePath string, withDiagnostics bool) ([]Diagnostic, error) {
	mu.Lock()
	// Lazy start: spawn gopls on first use
	if client == nil {
		if err := lazyStart(); err != nil {
			mu.Unlock()
			return nil, err
		}
	}
	mu.Unlock()

	// Build file URI
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, err
	}
	uri := "file://" + absPath

	if !withDiagnostics {
		// Fire-and-forget: just send didOpen, no waiting
		tlog.Debug("lsp.touch", "warmup", "file", absPath)
		if err := client.NotifyOpen(uri); err != nil {
			log.Printf("LSP warmup: notify open: %v", err)
		}
		return nil, nil
	}

	// With diagnostics: open and wait
	tlog.Debug("lsp.touch", "diagnostics", "file", absPath)
	diags, err := client.Diagnostics(uri, "")
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
	type sig struct{ line, col int; msg string }
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

// lazyStart starts the LSP server for the detected project language.
func lazyStart() error {
	tlog.Info("lsp.touch", "lazy_start", "root", projectRoot)
	lang := DetectLanguage(projectRoot)
	if lang == "" {
		lang = "go" // fallback
	}
	cfg := FindConfig(lang)
	if cfg == nil {
		return fmt.Errorf("no LSP server configured for %s", lang)
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
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
		log.Printf("LSP start: gopls not found: %v", err)
		lspAvailable = false
		return err
	}

	conn = NewConn(stdin, stdout)
	c := NewClient(conn)
	rootURI := "file://" + projectRoot
	if err := c.Initialize(rootURI); err != nil {
		log.Printf("LSP init: %v", err)
		cmd.Process.Kill()
		lspAvailable = false
		return err
	}

	client = c
	lspAvailable = true
	log.Printf("LSP: gopls started for %s", projectRoot)
	return nil
}

// NotifyOpen sends a didOpen notification for a file.
func (c *Client) NotifyOpen(uri string) error {
	return c.conn.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": "go",
			"version":    1,
		},
	})
}
