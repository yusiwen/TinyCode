package lsp

import (
	"context"
	"os"
	"testing"
)

func TestGoplsLifecycle(t *testing.T) {
	srv, err := Start(context.Background(), "gopls")
	if err != nil {
		t.Fatalf("Start gopls: %v", err)
	}
	defer srv.Close()

	// Initialize
	fileURI := "file:///home/yusiwen/git/ai/TinyCode"
	err = srv.Client.Initialize(fileURI)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	t.Log("Initialize OK")

	// Read source file
	src, err := os.ReadFile("/home/yusiwen/git/ai/TinyCode/agent/agent.go")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	t.Logf("Read source: %d bytes", len(src))

	// didOpen
	targetURI := "file:///home/yusiwen/git/ai/TinyCode/agent/agent.go"
	err = srv.Conn.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        targetURI,
			"languageId": "go",
			"version":    1,
			"text":       string(src),
		},
	})
	if err != nil {
		t.Fatalf("didOpen: %v", err)
	}
	t.Log("didOpen OK")

	// Try definition at line 12 (0-indexed), which is "type Agent struct {"
	var syms []SymbolInformation

	// Try definition at line 12 (0-indexed), which is "type Agent struct {"
	loc, err := srv.Client.GoToDefinition(targetURI, 12, 5)
	if err != nil {
		t.Fatalf("GoToDefinition error: %v", err)
	}
	if loc == nil {
		t.Log("Definition returned nil (may be a local struct)")

		// Try getting document symbols instead
		syms, err = srv.Client.DocumentSymbols(targetURI)
		if err != nil {
			t.Fatalf("DocumentSymbols error: %v", err)
		}
		if len(syms) == 0 {
			t.Fatal("DocumentSymbols returned no symbols")
		}
		t.Logf("DocumentSymbols: %d symbols found", len(syms))
		for _, sym := range syms {
			t.Logf("  %s (kind=%d) at line %d", sym.Name, sym.Kind, sym.Location.Range.Start.Line+1)
		}
	} else {
		t.Logf("Definition: %s:%d:%d", loc.URI, loc.Range.Start.Line+1, loc.Range.Start.Character+1)
		// Also get symbols for logging
		syms, _ = srv.Client.DocumentSymbols(targetURI)
	}

	// Test hover - try the word "Agent" at line 13 (0-indexed: 12), character 6
	hover, err := srv.Client.Hover(targetURI, 12, 6)
	if err != nil {
		t.Fatalf("Hover error: %v", err)
	}
	if hover == nil {
		t.Log("Hover returned nil - checking DocumentSymbols as fallback")

		// Try references on first symbol
		if len(syms) > 0 {
			first := syms[0]
			refs, err := srv.Client.FindReferences(targetURI,
				first.Location.Range.Start.Line,
				first.Location.Range.Start.Character)
			if err != nil {
				t.Logf("FindReferences error: %v", err)
			} else {
				t.Logf("FindReferences: %d results", len(refs))
			}
		}
	} else {
		t.Logf("Hover: %s", hover.Contents.Value[:min(200, len(hover.Contents.Value))])
	}
}
