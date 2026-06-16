package tool

import (
	"encoding/json"
	"testing"

	"github.com/yusiwen/tinycode/config"
)

func TestConnectMCPServersEmpty(t *testing.T) {
	tools, err := ConnectMCPServers(nil)
	if err != nil {
		t.Fatalf("expected no error for nil config, got: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestConnectMCPServersEmptyList(t *testing.T) {
	tools, err := ConnectMCPServers([]config.MCPServerConfig{})
	if err != nil {
		t.Fatalf("expected no error for empty list, got: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestParseMCPSchemaBasic(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)
	schema := parseMCPSchema(raw)
	if schema == nil {
		t.Fatal("expected non-nil schema")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok || props == nil {
		t.Fatal("expected properties")
	}
	textProp, ok := props["text"].(map[string]any)
	if !ok {
		t.Fatal("expected text property")
	}
	if textProp["type"] != "string" {
		t.Errorf("expected string type, got %v", textProp["type"])
	}
}

func TestParseMCPSchemaInvalid(t *testing.T) {
	schema := parseMCPSchema(json.RawMessage(`invalid`))
	if schema != nil {
		t.Errorf("expected nil for invalid JSON, got %v", schema)
	}
}

func TestParseMCPSchemaEmpty(t *testing.T) {
	schema := parseMCPSchema(nil)
	if schema != nil {
		t.Errorf("expected nil for nil input, got %v", schema)
	}
}

func TestCheckMCPURLLocalhost(t *testing.T) {
	if err := checkMCPURL("http://localhost:9000/mcp"); err != nil {
		t.Fatalf("expected no error for localhost, got: %v", err)
	}
}

func TestCheckMCPURLPublic(t *testing.T) {
	if err := checkMCPURL("https://example.com/mcp"); err != nil {
		t.Fatalf("expected no error for public, got: %v", err)
	}
}

func TestCheckMCPURLPrivate(t *testing.T) {
	err := checkMCPURL("http://192.168.1.1:9000/mcp")
	if err == nil {
		t.Fatal("expected error for private IP")
	}
}

func TestCheckMCPURLBadScheme(t *testing.T) {
	err := checkMCPURL("ftp://localhost/mcp")
	if err == nil {
		t.Fatal("expected error for non-http scheme")
	}
}

func TestCheckMCPURLInvalid(t *testing.T) {
	err := checkMCPURL(":::not-a-url:::")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}
