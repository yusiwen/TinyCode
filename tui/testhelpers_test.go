package tui

// Test helpers that belong to the project rather than to visual verification: the
// streaming tests use waitForStream, the geometry test uses frameDiff, and the golden
// tests pin the colour profile. Everything the visual harness used to provide now
// comes from tuiprobe — see verification_test.go.

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/yusiwen/TinyCode/tuiprobe/golden"
)

var shotTimeout = 20 * time.Second

func waitForStream(t *testing.T, out *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(shotTimeout)
	for {
		if strings.Contains(out.String(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream never contained %q within %s\nstream tail:\n%s",
				want, shotTimeout, stripANSIView(tail(out.String(), 800)))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// frameDiff reports the first differing line, which is what a reader needs. The
// comparison itself lives in tuiprobe/golden, so a golden failure and a geometry-test
// failure explain themselves the same way.
func frameDiff(want, got string) string {
	return golden.Diff(want, got)
}

func withTrueColor(t *testing.T, fn func()) {
	t.Helper()
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	resetStyleCache()
	defer func() {
		lipgloss.SetColorProfile(previous)
		resetStyleCache()
	}()
	fn()
}

func resetStyleCache() {
	styleMu.Lock()
	styleCache = map[CellStyle]lipgloss.Style{}
	styleMu.Unlock()
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
