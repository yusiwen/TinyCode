package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-rod/rod"
)

// ── Browser detection chain ──

// Browser commands to try in order for system-installed Chromium.
// (browserCommands is declared in web_extract.go)

// playwrightChromiumPath returns the path to a Chromium installed by Playwright.
func playwrightChromiumPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// Platform-specific Playwright cache directory
	var playwrightDir string
	switch runtime.GOOS {
	case "darwin":
		playwrightDir = filepath.Join(home, "Library", "Caches", "ms-playwright")
	default:
		playwrightDir = filepath.Join(home, ".cache", "ms-playwright")
	}
	entries, err := os.ReadDir(playwrightDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "chromium-") {
			// Platform-specific Chrome binary path within Playwright
			var sub string
			switch runtime.GOOS {
			case "darwin":
				sub = filepath.Join(playwrightDir, e.Name(), "chrome-mac", "Chromium.app", "Contents", "MacOS", "Chromium")
			default:
				sub = filepath.Join(playwrightDir, e.Name(), "chrome-linux", "chrome")
			}
			if fi, err := os.Stat(sub); err == nil && fi.Mode().IsRegular() {
				return sub
			}
		}
	}
	return ""
}

// findBrowser returns the path to a usable Chromium/Chrome binary,
// or tries rod's auto-download as the final fallback.
// Returns empty string if nothing can be found.
func findBrowser() string {
	// 1. System-installed browsers
	for _, name := range browserCommands {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	// 2. Playwright-bundled Chromium
	if pw := playwrightChromiumPath(); pw != "" {
		return pw
	}
	return ""
}

// crawlViaExec uses a Chromium binary with --dump-dom to extract page content.
func crawlViaExec(ctx context.Context, browserPath, url string) (string, error) {
	ctx2, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()

	args := []string{
		"--headless",
		"--disable-gpu",
		"--no-sandbox",
	}
	// Linux-specific flags for headless server environments
	if runtime.GOOS == "linux" {
		args = append(args, "--disable-dev-shm-usage", "--single-process")
	}
	args = append(args, "--dump-dom", url)
	cmd := exec.CommandContext(ctx2, browserPath, args...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("exec: %w", err)
	}
	if len(output) < 200 {
		return "", fmt.Errorf("page too short (%d bytes)", len(output))
	}
	return processContent(string(output)), nil
}

// crawlViaRod uses go-rod to render the page (auto-downloads Chromium if needed).
func crawlViaRod(ctx context.Context, url string) (string, error) {
	// rod.NewBrowser() auto-downloads to ~/.cache/rod/ on first call
	browser := rod.New().MustConnect()
	defer browser.Close()

	page := browser.MustPage(url)
	defer page.Close()

	page.MustWaitLoad()

	// Scroll to trigger lazy-loaded content
	page.MustEval(`window.scrollTo(0, document.body.scrollHeight)`)
	page.MustWait("1s")
	page.MustEval(`window.scrollTo(0, 0)`)

	// Extract title and content
	title := page.MustEval(`document.title`).Str()
	content := page.MustEval(`document.body.innerText`).Str()

	if content == "" {
		return "", fmt.Errorf("no content extracted from %s", url)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n\n_Source: %s_\n\n---\n\n%s", title, url, content))
	return sb.String(), nil
}

// WebExtractBrowser returns a Tool that crawls web pages using a headless
// browser with JavaScript rendering support (handles SPA, SSR, anti-bot pages).
func WebExtractBrowser() Tool {
	return Tool{
		Name:        "web_extract_browser",
		Description: "Extract content from JS-rendered web pages using a headless browser. Handles SPAs, lazy loading, and Cloudflare-protected sites. Falls back to auto-downloaded Chromium if none is installed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The URL to crawl",
				},
				"mode": map[string]any{
					"type":        "string",
					"enum":        []any{"auto", "full"},
					"description": "'auto' extracts main article content (default), 'full' returns all page text",
				},
			},
			"required": []string{"url"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			url, _ := args["url"].(string)
			if url == "" {
				return "", fmt.Errorf("url is required")
			}
			mode, _ := args["mode"].(string)
			if mode == "" {
				mode = "auto"
			}

			// Try system/playwright Chromium first
			if browserPath := findBrowser(); browserPath != "" {
				content, err := crawlViaExec(ctx, browserPath, url)
				if err == nil {
					if mode == "full" {
						return content, nil
					}
					// Extract article content
					return extractArticle(mode, content, url), nil
				}
			}

			// Fallback to go-rod (auto-downloads Chromium)
			content, err := crawlViaRod(ctx, url)
			if err != nil {
				return "", fmt.Errorf("all browser methods failed: %w", err)
			}
			return content, nil
		},
	}
}

// extractArticle formats the content into a clean Markdown document.
func extractArticle(mode, content, url string) string {
	// Extract title from first <h1> or --- delimited line
	title := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			title = strings.TrimPrefix(trimmed, "# ")
			break
		}
	}
	if title == "" {
		title = url
	}

	// Find first paragraph (non-empty text block) as description
	desc := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 30 && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "---") {
			if len(trimmed) > 150 {
				desc = trimmed[:147] + "..."
			} else {
				desc = trimmed
			}
			break
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n", title))
	if desc != "" {
		sb.WriteString(fmt.Sprintf("\n> %s\n", desc))
	}
	sb.WriteString(fmt.Sprintf("\n_Source: %s_\n\n---\n\n", url))
	sb.WriteString(content)
	if len(sb.String()) > 5000 {
		return sb.String()[:5000] + "\n\n[... content truncated at 5000 chars ...]"
	}
	return sb.String()
}
