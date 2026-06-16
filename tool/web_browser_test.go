package tool

import (
	"context"
	"strings"
	"testing"
)

func TestPlaywrightChromiumPath(t *testing.T) {
	// Smoke test: should not panic, returns empty string normally
	path := playwrightChromiumPath()
	_ = path // no assertion — depends on test environment
}

func TestExtractArticle(t *testing.T) {
	result := extractArticle("auto", "Some content here\n## Heading\nMore text", "https://example.com")
	if !strings.Contains(result, "# ") {
		t.Errorf("expected title in output, got:\n%s", result[:100])
	}
	if !strings.Contains(result, "Source: https://example.com") {
		t.Errorf("expected source URL in output")
	}
}

func TestExtractArticleWithTitle(t *testing.T) {
	result := extractArticle("auto", "## This is a title\n\nBody content paragraph with enough text for description detection.\n\nMore text.", "https://test.dev")
	if !strings.Contains(result, "This is a title") {
		t.Errorf("expected title, got:\n%s", result[:100])
	}
}

func TestWebExtractBrowserTool(t *testing.T) {
	tool := WebExtractBrowser()
	if tool.Name != "web_extract_browser" {
		t.Errorf("expected 'web_extract_browser', got %q", tool.Name)
	}
	if tool.Parameters == nil {
		t.Fatal("expected non-nil Parameters")
	}
	props, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties")
	}
	if _, ok := props["url"]; !ok {
		t.Fatal("expected 'url' in properties")
	}
}

func TestWebExtractBrowserEmptyURL(t *testing.T) {
	tool := WebExtractBrowser()
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestFindBrowserNoChromium(t *testing.T) {
	path := findBrowser()
	_ = path // no assertion — depends on test environment
}

func TestCrawlViaExecError(t *testing.T) {
	_, err := crawlViaExec(context.Background(), "/nonexistent/chrome", "https://example.com")
	if err == nil {
		t.Fatal("expected error for non-existent browser")
	}
}
