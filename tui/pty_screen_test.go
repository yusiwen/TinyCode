package tui

import (
	"fmt"
	"html"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"github.com/mattn/go-runewidth"
	"github.com/yusiwen/tinycode/tool"
)

// screenBuffer is the smallest terminal emulator that can replay what the TUI's
// renderer writes to a real terminal. It exists so a screenshot can be taken
// from the live PTY stream (issue #16) instead of from View()'s return value:
// a regression that only lives in the renderer - a repaint that never happens,
// a line that is never erased, cursor addressing that lands one row off - is
// invisible to the frame functions but shows up here.
//
// The vocabulary is what Bubble Tea's standard renderer actually emits, measured
// from a captured 80x24 welcome stream: `\r` and `\r\n` line breaks, `ESC[K`
// erases, `ESC[<n>A` cursor-up repaints, `ESC[6n` / `ESC[?25l` / `ESC[?2004h` /
// `ESC[?1003h` / `ESC[?1006h` mode changes, the `ESC]11;?` background query and
// SGR styling. Cursor-addressed writes are supported as well (`ESC[<n>;<m>H`,
// `ESC[<n>G`, `ESC[<n>J`) even though the current renderer does not need them,
// so a renderer change fails loudly instead of silently producing garbage.
type screenBuffer struct {
	width, height int
	cells         []screenCell
	row, col      int
	pendingWrap   bool
	style         ansiState
}

// screenCell is one character cell of the terminal.
type screenCell struct {
	r    rune
	st   ansiState
	cont bool // trailing half of a double-width rune
}

func newScreenBuffer(width, height int) *screenBuffer {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	return &screenBuffer{width: width, height: height, cells: make([]screenCell, width*height)}
}

func (s *screenBuffer) idx(row, col int) int { return row*s.width + col }

// scrollUp drops the top row when the cursor moves past the last one.
func (s *screenBuffer) scrollUp() {
	if s.row < s.height {
		return
	}
	copy(s.cells, s.cells[s.width:])
	for i := (s.height - 1) * s.width; i < s.height*s.width; i++ {
		s.cells[i] = screenCell{}
	}
	s.row = s.height - 1
}

func (s *screenBuffer) put(r rune) {
	if s.pendingWrap {
		s.pendingWrap = false
		s.col = 0
		s.row++
		s.scrollUp()
	}
	if s.row < 0 {
		s.row = 0
	}
	if s.col < 0 {
		s.col = 0
	}
	if s.col >= s.width {
		s.col = s.width - 1
	}
	w := runewidth.RuneWidth(r)
	if w < 1 {
		w = 1
	}
	s.cells[s.idx(s.row, s.col)] = screenCell{r: r, st: s.style}
	if w == 2 && s.col+1 < s.width {
		s.cells[s.idx(s.row, s.col+1)] = screenCell{cont: true, st: s.style}
	}
	if s.col+w >= s.width {
		s.col = s.width - 1
		s.pendingWrap = true
		return
	}
	s.col += w
}

// eraseLine clears part of one row: 0 = cursor to end, 1 = start to cursor,
// 2 = the whole row.
func (s *screenBuffer) eraseLine(row, mode int) {
	if row < 0 || row >= s.height {
		return
	}
	from, to := 0, s.width-1
	switch mode {
	case 0:
		from = s.col
	case 1:
		to = s.col
	}
	for c := from; c <= to; c++ {
		s.cells[s.idx(row, c)] = screenCell{}
	}
}

// eraseDisplay clears part of the screen: 0 = cursor to end, 1 = start to
// cursor, 2 = everything (and the cursor goes home).
func (s *screenBuffer) eraseDisplay(mode int) {
	switch mode {
	case 2:
		for i := range s.cells {
			s.cells[i] = screenCell{}
		}
		s.row, s.col, s.pendingWrap = 0, 0, false
	case 0:
		s.eraseLine(s.row, 0)
		for r := s.row + 1; r < s.height; r++ {
			s.cells[s.idx(r, 0)] = screenCell{}
			for c := 0; c < s.width; c++ {
				s.cells[s.idx(r, c)] = screenCell{}
			}
		}
	case 1:
		for r := 0; r < s.row; r++ {
			for c := 0; c < s.width; c++ {
				s.cells[s.idx(r, c)] = screenCell{}
			}
		}
		s.eraseLine(s.row, 1)
	}
}

// Write replays a chunk of terminal output.
func (s *screenBuffer) Write(p []byte) (int, error) {
	for i := 0; i < len(p); {
		switch {
		case p[i] == 0x1b && i+1 < len(p) && p[i+1] == '[':
			j := i + 2
			for j < len(p) && !(p[j] >= 0x40 && p[j] <= 0x7e) {
				j++
			}
			if j >= len(p) {
				return len(p), nil // truncated sequence: ignore the tail
			}
			s.csi(string(p[i+2:j]), p[j])
			i = j + 1
		case p[i] == 0x1b && i+1 < len(p) && p[i+1] == ']':
			j := i + 2
			for j < len(p) && p[j] != 0x07 && !(p[j] == 0x1b && j+1 < len(p) && p[j+1] == '\\') {
				j++
			}
			switch {
			case j >= len(p):
				return len(p), nil
			case p[j] == 0x07:
				i = j + 1
			default:
				i = j + 2
			}
		case p[i] == 0x1b:
			i += 2 // two-byte escape (charset selection, keypad modes)
		case p[i] == '\r':
			s.col, s.pendingWrap = 0, false
			i++
		case p[i] == '\n':
			s.row++
			s.pendingWrap = false
			s.scrollUp()
			i++
		case p[i] == '\b':
			if s.col > 0 {
				s.col--
			}
			s.pendingWrap = false
			i++
		case p[i] == '\t':
			s.col = min((s.col/8+1)*8, s.width-1)
			i++
		case p[i] < 0x20:
			i++ // other control characters carry no visible state here
		default:
			r, size := utf8.DecodeRune(p[i:])
			if r == utf8.RuneError && size == 1 {
				i++
				continue
			}
			s.put(r)
			i += size
		}
	}
	return len(p), nil
}

// csi applies one CSI sequence.
func (s *screenBuffer) csi(raw string, final byte) {
	params := parseInts(raw)
	first := 0
	if len(params) > 0 {
		first = params[0]
	}
	switch final {
	case 'm':
		s.style = applySGR(params, s.style)
	case 'A':
		s.row = max(s.row-max(first, 1), 0)
		s.pendingWrap = false
	case 'B':
		s.row += max(first, 1)
		s.scrollUp()
		s.pendingWrap = false
	case 'C':
		s.col = min(s.col+max(first, 1), s.width-1)
		s.pendingWrap = false
	case 'D':
		s.col = max(s.col-max(first, 1), 0)
		s.pendingWrap = false
	case 'G':
		s.col = min(max(first, 1)-1, s.width-1)
		s.pendingWrap = false
	case 'H', 'f':
		row, col := 1, 1
		if len(params) > 0 {
			row = max(params[0], 1)
		}
		if len(params) > 1 {
			col = max(params[1], 1)
		}
		s.row = min(row-1, s.height-1)
		s.col = min(col-1, s.width-1)
		s.pendingWrap = false
	case 'J':
		s.eraseDisplay(first)
	case 'K':
		s.eraseLine(s.row, first)
	}
	// Everything else (mode set/reset, device status, window ops) changes no
	// cell content, which is all this buffer reproduces.
}

// String renders the visible screen as plain text with trailing blanks removed,
// the form the frame goldens use.
func (s *screenBuffer) String() string {
	var b strings.Builder
	for row := 0; row < s.height; row++ {
		var line strings.Builder
		for col := 0; col < s.width; col++ {
			cell := s.cells[s.idx(row, col)]
			if cell.cont {
				continue
			}
			if cell.r == 0 {
				line.WriteByte(' ')
				continue
			}
			line.WriteRune(cell.r)
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// HTML renders the visible screen as the body of the screenshot page, keeping
// the styling the stream carried.
func (s *screenBuffer) HTML() string {
	var body strings.Builder
	for row := 0; row < s.height; row++ {
		// Right-trim before emitting spans so the image has no dead margin.
		end := s.width
		for end > 0 {
			cell := s.cells[s.idx(row, end-1)]
			if cell.r != 0 && cell.r != ' ' {
				break
			}
			end--
		}
		for col := 0; col < end; {
			cell := s.cells[s.idx(row, col)]
			if cell.cont {
				col++
				continue
			}
			run := make([]rune, 0, 8)
			style := cell.st
			for col < end {
				next := s.cells[s.idx(row, col)]
				if next.cont {
					col++
					continue
				}
				if next.st != style {
					break
				}
				if next.r == 0 {
					run = append(run, ' ')
				} else {
					run = append(run, next.r)
				}
				col++
			}
			text := html.EscapeString(string(run))
			if attr := style.styleAttr(); attr != "" {
				fmt.Fprintf(&body, `<span style="%s">%s</span>`, attr, text)
			} else {
				body.WriteString(text)
			}
		}
		body.WriteByte('\n')
	}
	return body.String()
}

// parseInts parses CSI parameters, treating an empty or malformed field as 0
// the way the SGR and cursor sequences here do.
func parseInts(raw string) []int {
	if raw == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == ':' })
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n := 0
		for _, r := range f {
			if r < '0' || r > '9' {
				n = 0
				break
			}
			n = n*10 + int(r-'0')
		}
		out = append(out, n)
	}
	return out
}

// --- Replay fidelity ------------------------------------------------------

// replayScreen feeds a captured terminal stream through a fresh buffer.
func replayScreen(stream string, width, height int) *screenBuffer {
	s := newScreenBuffer(width, height)
	_, _ = s.Write([]byte(stream))
	return s
}

// TestScreenBufferReplaysViewFrame is the fidelity check that needs no PTY: a
// frame rendered by View() must survive the round trip through the emulator.
// The renderer writes CRLF between rows (measured from a captured stream); a
// bare LF would leave the cursor in the last column, which is why the frame's
// own newlines are normalized first.
func TestScreenBufferReplaysViewFrame(t *testing.T) {
	for _, sc := range []struct {
		name  string
		size  frameSize
		build func(w, h int) *TuiModel
	}{
		{"welcome", frameSize{80, 24}, frameWelcome},
		{"markdown", frameSize{80, 24}, frameMarkdown},
		{"todo", frameSize{80, 24}, frameTodo},
		{"longoutput", frameSize{120, 40}, frameLongOutput},
	} {
		t.Run(sc.name, func(t *testing.T) {
			var frame string
			withTrueColor(t, func() {
				frame = sc.build(sc.size.W, sc.size.H).View()
			})

			screen := replayScreen(strings.ReplaceAll(frame, "\n", "\r\n"), sc.size.W, sc.size.H)

			if got, want := screen.String(), normalizeFrame(frame); got != want {
				t.Errorf("replayed screen differs from the frame\n%s", frameDiff(want, got))
			}
			if !strings.Contains(screen.HTML(), "<span style=") {
				t.Error("replayed screen lost its styling")
			}
		})
	}
}

func TestScreenBufferCursorUpRepaints(t *testing.T) {
	s := newScreenBuffer(5, 3)
	_, _ = s.Write([]byte("aaa\r\nbbb\r\nccc"))
	if got := firstRow(s); got != "aaa" {
		t.Fatalf("row 0 = %q, want aaa", got)
	}
	_, _ = s.Write([]byte("\x1b[2A\rXXX"))
	if got := firstRow(s); got != "XXX" {
		t.Errorf("row 0 after repaint = %q, want XXX", got)
	}
	if got := strings.Split(s.String(), "\n")[2]; got != "ccc" {
		t.Errorf("row 2 = %q, want ccc (the repaint touched the wrong row)", got)
	}
}

func TestScreenBufferErasesStaleText(t *testing.T) {
	s := newScreenBuffer(10, 2)
	_, _ = s.Write([]byte("abcdefghij\r\nkl"))
	_, _ = s.Write([]byte("\x1b[1A\r\x1b[K"))
	if got := firstRow(s); got != "" {
		t.Errorf("row 0 = %q, want it erased end-of-line", got)
	}
	if got := strings.Split(s.String(), "\n")[1]; got != "kl" {
		t.Errorf("row 1 = %q, want kl", got)
	}
}

func TestScreenBufferScrolls(t *testing.T) {
	s := newScreenBuffer(4, 2)
	_, _ = s.Write([]byte("aaa\r\nbbb\r\nccc"))
	rows := strings.Split(strings.TrimRight(s.String(), "\n"), "\n")
	if len(rows) != 2 || rows[0] != "bbb" || rows[1] != "ccc" {
		t.Errorf("rows after scrolling = %q, want [bbb ccc]", rows)
	}
}

func TestScreenBufferWrapsAtTheLastColumn(t *testing.T) {
	// A full row plus one more character scrolls a two-row screen: the terminal
	// wraps on the next printable rune, exactly like the renderer relies on.
	s := newScreenBuffer(4, 2)
	_, _ = s.Write([]byte("one\r\ntwo\r\nthree"))
	rows := strings.Split(strings.TrimRight(s.String(), "\n"), "\n")
	if len(rows) != 2 || rows[0] != "thre" || rows[1] != "e" {
		t.Errorf("rows = %q, want [thre e]", rows)
	}
}

func TestScreenBufferWideRunes(t *testing.T) {
	s := newScreenBuffer(6, 1)
	_, _ = s.Write([]byte("中文x"))
	if got := firstRow(s); got != "中文x" {
		t.Errorf("row = %q, want 中文x", got)
	}
	// A double-width rune must not leave a stray continuation cell in the
	// HTML: the body is exactly the three runes.
	if body := s.HTML(); strings.Count(body, "中") != 1 || strings.Count(body, "文") != 1 {
		t.Errorf("wide runes were duplicated in the HTML: %q", body)
	}
}

func TestScreenBufferIgnoresQueriesAndModes(t *testing.T) {
	s := newScreenBuffer(10, 1)
	_, _ = s.Write([]byte("\x1b]11;?\x1b\\\x1b[6n\x1b[?25l\x1b[?2004hAB"))
	if got := firstRow(s); got != "AB" {
		t.Errorf("row = %q, want AB (queries and mode changes carry no text)", got)
	}
}

func TestScreenBufferEraseDisplay(t *testing.T) {
	s := newScreenBuffer(5, 2)
	_, _ = s.Write([]byte("abc\r\ndef\x1b[2J"))
	if got := strings.TrimSpace(s.String()); got != "" {
		t.Errorf("screen = %q, want it cleared", got)
	}
}

// firstRow returns row 0 with trailing blanks removed.
func firstRow(s *screenBuffer) string {
	return strings.Split(s.String(), "\n")[0]
}

// --- Live-stream screenshot (gated) ---------------------------------------

// stableBannerRows picks the welcome-frame rows whose text cannot depend on the
// build (tool/skill/agent counts) or on the wall clock (session duration): the
// wordmark, the shortcut block, the footer and anything else without a digit.
// Those rows must survive the round trip through a real terminal byte for byte;
// the counts row and the status bar legitimately differ in a live run.
func stableBannerRows(frame string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimRight(frame, "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.ContainsAny(trimmed, "0123456789") {
			continue
		}
		out = append(out, strings.TrimRight(line, " "))
	}
	return out
}

// missingRows returns the wanted rows the replayed screen does not show.
func missingRows(screen *screenBuffer, want []string) []string {
	have := map[string]bool{}
	for _, line := range strings.Split(screen.String(), "\n") {
		have[strings.TrimRight(line, " ")] = true
	}
	var missing []string
	for _, line := range want {
		if !have[line] {
			missing = append(missing, line)
		}
	}
	return missing
}

// TestBinaryScreenshotFromStream takes the screenshot from the live PTY stream
// instead of from View(): the bytes the running renderer wrote are replayed into
// a screen buffer and that screen is rendered to PNG. It is slower and gated by
// TUI_SHOT=1, which is exactly why the frame-function screenshots still exist
// for the scenarios a live binary cannot reach without driving the whole app.
func TestBinaryScreenshotFromStream(t *testing.T) {
	requireShot(t)

	browserPath := tool.FindBrowser()
	if browserPath == "" {
		t.Skip("no Chromium/Chrome installed (system or the Playwright cache)")
	}

	var want string
	withTrueColor(t, func() {
		want = normalizeFrame(frameWelcome(80, 24).View())
	})
	stable := stableBannerRows(want)
	if len(stable) == 0 {
		t.Fatal("the welcome frame has no build- and clock-independent rows to compare against")
	}

	smoke := startBinaryPTY(t, &pty.Winsize{Rows: 24, Cols: 80})

	// The wait is the assertion: poll the replay until the terminal really shows
	// the frame, so a fixed sleep never decides whether a repaint landed.
	var screen *screenBuffer
	var missing []string
	deadline := time.Now().Add(shotTimeout)
	for {
		screen = replayScreen(smoke.out.String(), 80, 24)
		missing = missingRows(screen, stable)
		if len(missing) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the replayed screen never showed the frame within %s; still missing:\n  %s\nreplayed:\n%s",
				shotTimeout, strings.Join(missing, "\n  "), screen.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !sgrSequence.MatchString(smoke.out.String()) {
		t.Error("the stream carried no SGR styling: termenv did not see the PTY as a colour terminal")
	}
	if !strings.Contains(screen.HTML(), "<span style=") {
		t.Error("the replayed screen lost its styling")
	}
	if got := screen.String(); !strings.Contains(got, "session:") {
		t.Errorf("the replayed status bar is missing:\n%s", got)
	}

	launcher := newShotLauncher(t, browserPath)
	browser := connectBrowser(t, launcher.MustLaunch())
	defer func() {
		if err := runStage(func() error {
			return browser.Timeout(shotTimeout).Close()
		}); err != nil {
			t.Errorf("close browser: %v", err)
		}
	}()

	name := "tinycode-pty-welcome-80x24"
	if err := runStage(func() error {
		return capturePNG(browser.Timeout(shotTimeout), shotDir(t), name,
			htmlDocument(screen.HTML()), frameSize{80, 24})
	}); err != nil {
		t.Fatalf("screenshot replayed from the live stream: %v", err)
	}
	t.Logf("wrote %s/%s.png", shotDir(t), name)

	smoke.quit(t)
}
