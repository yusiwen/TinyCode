package lsp

import (
	"encoding/json"
	"fmt"
)

// Client is a high-level LSP client wrapping the JSON-RPC connection.
type Client struct {
	conn *Conn
}

// NewClient creates an LSP client using the given connection.
func NewClient(conn *Conn) *Client {
	return &Client{conn: conn}
}

// Initialize sends the initialize request and returns server capabilities.
func (c *Client) Initialize(rootURI string) error {
	params := map[string]any{
		"processId": nil,
		"rootUri":   rootURI,
		"capabilities": map[string]any{},
	}

	_, err := c.conn.Send("initialize", params)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	// Send initialized notification (no response expected)
	if err := c.conn.Notify("initialized", map[string]any{}); err != nil {
		return fmt.Errorf("initialized notification: %w", err)
	}

	return nil
}

// Shutdown sends the shutdown request and exit notification.
func (c *Client) Shutdown() error {
	// Some LSP servers respond to shutdown with null
	c.conn.Send("shutdown", nil)
	c.conn.Notify("exit", nil)
	return nil
}

// GoToDefinition returns the location of the definition for a symbol.
func (c *Client) GoToDefinition(uri string, line, character int) (*Location, error) {
	params := map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position": map[string]int{
			"line":      line,
			"character": character,
		},
	}

	raw, err := c.conn.Send("textDocument/definition", params)
	if err != nil {
		return nil, err
	}

	// Response can be Location | Location[] | null
	var loc Location
	if err := json.Unmarshal(raw, &loc); err == nil && loc.URI != "" {
		return &loc, nil
	}

	var locs []Location
	if err := json.Unmarshal(raw, &locs); err == nil && len(locs) > 0 {
		return &locs[0], nil
	}

	return nil, nil
}

// FindReferences returns all references to a symbol.
func (c *Client) FindReferences(uri string, line, character int) ([]Location, error) {
	params := map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position": map[string]int{
			"line":      line,
			"character": character,
		},
		"context": map[string]bool{
			"includeDeclaration": true,
		},
	}

	raw, err := c.conn.Send("textDocument/references", params)
	if err != nil {
		return nil, err
	}

	var locs []Location
	if err := json.Unmarshal(raw, &locs); err != nil {
		return nil, err
	}
	return locs, nil
}

// Hover returns hover information for a symbol.
func (c *Client) Hover(uri string, line, character int) (*Hover, error) {
	params := map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position": map[string]int{
			"line":      line,
			"character": character,
		},
	}

	raw, err := c.conn.Send("textDocument/hover", params)
	if err != nil {
		return nil, err
	}

	var hover Hover
	if err := json.Unmarshal(raw, &hover); err != nil {
		return nil, nil // null response = no hover info
	}
	return &hover, nil
}

// DocumentSymbols returns all symbols in a document.
func (c *Client) DocumentSymbols(uri string) ([]SymbolInformation, error) {
	params := map[string]any{
		"textDocument": map[string]string{"uri": uri},
	}

	raw, err := c.conn.Send("textDocument/documentSymbol", params)
	if err != nil {
		return nil, err
	}

	// Response can be SymbolInformation[] or DocumentSymbol[]
	var syms []SymbolInformation
	if err := json.Unmarshal(raw, &syms); err != nil {
		// Try flat SymbolInformation format
		var flat []SymbolInformation
		if err2 := json.Unmarshal(raw, &flat); err2 == nil {
			return flat, nil
		}
	}
	return syms, nil
}

// Diagnostics returns diagnostics (errors/warnings) for a document.
// NOTE: LSP diagnostics are pushed from server as notifications, not requested.
// This method opens a file temporarily and waits for the diagnostic push.
func (c *Client) Diagnostics(uri string, content string) ([]Diagnostic, error) {
	// Open the document
	if err := c.conn.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": "go",
			"version":    1,
			"text":       content,
		},
	}); err != nil {
		return nil, err
	}

	// Read the pushed textDocument/publishDiagnostics notification
	body, err := c.conn.readMessage()
	if err != nil {
		return nil, err
	}

	var base struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &base); err != nil {
		return nil, err
	}

	if base.Method != "textDocument/publishDiagnostics" {
		return nil, nil
	}

	var params struct {
		URI         string       `json:"uri"`
		Diagnostics []Diagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(base.Params, &params); err != nil {
		return nil, err
	}

	return params.Diagnostics, nil
}

// Diagnostic represents a single diagnostic (error/warning).
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Message  string `json:"message"`
}
