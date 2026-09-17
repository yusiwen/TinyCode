package tool

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFixture creates a file at dir/rel, creating parent directories.
func writeFixture(t *testing.T, dir, rel string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return path
}

// TestFindPlaywrightBrowser covers the layouts Playwright uses. The old
// detection only knew `chrome-mac/Chromium.app`, so every current installation
// was invisible and the browser tool silently downloaded its own Chromium.
func TestFindPlaywrightBrowser(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		fixture string
		want    string
	}{
		{
			name:    "darwin google chrome for testing arm64",
			goos:    "darwin",
			fixture: "chromium-1243/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
			want:    "chromium-1243/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
		},
		{
			name:    "darwin google chrome for testing x64",
			goos:    "darwin",
			fixture: "chromium-1243/chrome-mac-x64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
			want:    "chromium-1243/chrome-mac-x64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
		},
		{
			name:    "darwin legacy chromium.app",
			goos:    "darwin",
			fixture: "chromium-1000/chrome-mac/Chromium.app/Contents/MacOS/Chromium",
			want:    "chromium-1000/chrome-mac/Chromium.app/Contents/MacOS/Chromium",
		},
		{
			name:    "darwin headless shell only",
			goos:    "darwin",
			fixture: "chromium_headless_shell-1243/chrome-headless-shell-mac-arm64/chrome-headless-shell",
			want:    "chromium_headless_shell-1243/chrome-headless-shell-mac-arm64/chrome-headless-shell",
		},
		{
			name:    "linux current",
			goos:    "linux",
			fixture: "chromium-1243/chrome-linux64/chrome",
			want:    "chromium-1243/chrome-linux64/chrome",
		},
		{
			name:    "linux legacy",
			goos:    "linux",
			fixture: "chromium-1000/chrome-linux/chrome",
			want:    "chromium-1000/chrome-linux/chrome",
		},
		{
			name:    "windows full browser preferred over shell",
			goos:    "windows",
			fixture: "chromium-1243/chrome-win/chrome.exe",
			want:    "chromium-1243/chrome-win/chrome.exe",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cache := t.TempDir()
			writeFixture(t, cache, tc.fixture)

			got := findPlaywrightBrowser(cache, tc.goos)
			if got != filepath.Join(cache, tc.want) {
				t.Fatalf("findPlaywrightBrowser = %q, want %q", got, filepath.Join(cache, tc.want))
			}
		})
	}
}

// TestFindPlaywrightBrowserPrefersNewest checks the revision ordering and that a
// package directory without a browser binary is skipped.
func TestFindPlaywrightBrowserPrefersNewest(t *testing.T) {
	cache := t.TempDir()
	writeFixture(t, cache, "chromium-1000/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing")
	newest := writeFixture(t, cache, "chromium-1243/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing")
	if err := os.MkdirAll(filepath.Join(cache, "chromium-1300", "chrome-mac-arm64"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, cache, "chromium-1100/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing")
	// A three-digit revision must not outrank a four-digit one.
	writeFixture(t, cache, "chromium-999/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing")
	writeFixture(t, cache, "chromium_headless_shell-1243/chrome-headless-shell-mac-arm64/chrome-headless-shell")

	if got := findPlaywrightBrowser(cache, "darwin"); got != newest {
		t.Fatalf("findPlaywrightBrowser = %q, want the newest full browser %q", got, newest)
	}
}

// TestFindPlaywrightBrowserEmpty covers the no-installation cases.
func TestFindPlaywrightBrowserEmpty(t *testing.T) {
	if got := findPlaywrightBrowser(filepath.Join(t.TempDir(), "missing"), "darwin"); got != "" {
		t.Errorf("missing cache dir = %q, want empty", got)
	}

	cache := t.TempDir()
	writeFixture(t, cache, "ffmpeg-1011/ffmpeg-mac")
	if got := findPlaywrightBrowser(cache, "darwin"); got != "" {
		t.Errorf("cache without a browser = %q, want empty", got)
	}
}

// TestPlaywrightCacheDir pins the per-platform cache location.
func TestPlaywrightCacheDir(t *testing.T) {
	if got := playwrightCacheDir("/home/u", "darwin"); got != "/home/u/Library/Caches/ms-playwright" {
		t.Errorf("darwin cache dir = %q", got)
	}
	if got := playwrightCacheDir("/home/u", "linux"); got != "/home/u/.cache/ms-playwright" {
		t.Errorf("linux cache dir = %q", got)
	}
}
