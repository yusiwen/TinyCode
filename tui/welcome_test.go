package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/tool"
)

// plainWidth returns the rendered width of a laid-out banner line.
func plainWidth(line []CellChunk) int {
	w := 0
	for _, c := range line {
		w += lipgloss.Width(c.Text)
	}
	return w
}

func plainText(line []CellChunk) string {
	var b strings.Builder
	for _, c := range line {
		b.WriteString(c.Text)
	}
	return b.String()
}

func TestWelcomeBannerFullLayout(t *testing.T) {
	const width = 100
	lines := renderWelcomeLines(welcomeInfo{Tools: 24, Skills: 2, Agents: 6}, width)
	if got := len(lines); got != 6+1+1+1+1+5+1+2 {
		t.Fatalf("banner row count = %d, want 18", got)
	}

	// A wide viewport shows the ASCII wordmark verbatim, one row per line.
	for i, want := range welcomeArt {
		if got := plainText(lines[i]); got != welcomeIndent+want {
			t.Errorf("wordmark row %d = %q, want %q", i, got, welcomeIndent+want)
		}
	}
	if got := plainText(lines[len(welcomeArt)]); got != "" {
		t.Errorf("expected a blank row after the wordmark, got %q", got)
	}

	// Every row must fit the viewport, and counts/commands must be rendered.
	var joined strings.Builder
	for _, line := range lines {
		if w := plainWidth(line); w > width {
			t.Errorf("banner line %q is %d wide, viewport is %d", plainText(line), w, width)
		}
		joined.WriteString(plainText(line))
		joined.WriteString("\n")
	}
	all := joined.String()
	for _, want := range []string{"24 tools", "2 skills", "6 agents", "Get started", "/help", "/model", "/plan /build", "Ctrl+J", "~/.tinycode/config.json", "github.com/yusiwen/TinyCode"} {
		if !strings.Contains(all, want) {
			t.Errorf("banner is missing %q", want)
		}
	}

	// Command rows must pad the key column so descriptions line up.
	for _, c := range welcomeCommands {
		want := welcomeIndent + c.Key + strings.Repeat(" ", welcomeKeyWidth-len(c.Key)) + "  " + c.Desc
		found := false
		for _, line := range lines {
			if plainText(line) == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing aligned command row %q", want)
		}
	}
}

func TestWelcomeBannerNarrowLayout(t *testing.T) {
	const width = 40
	lines := renderWelcomeLines(welcomeInfo{Tools: 3, Skills: 1, Agents: 6}, width)
	if len(lines) == 0 {
		t.Fatal("narrow banner rendered no rows")
	}
	if !strings.Contains(plainText(lines[0]), "TinyCode") {
		t.Errorf("narrow banner title = %q, want it to contain TinyCode", plainText(lines[0]))
	}
	if strings.Contains(plainText(lines[0]), "|_   _|") {
		t.Error("narrow banner should drop the ASCII wordmark")
	}
	for _, line := range lines {
		if w := plainWidth(line); w > width {
			t.Errorf("narrow banner line %q is %d wide, viewport is %d", plainText(line), w, width)
		}
	}
}

func TestWelcomeBannerRendersIntoGrid(t *testing.T) {
	info := welcomeInfo{Tools: 24, Skills: 2, Agents: 6}
	lines := renderWelcomeLines(info, 80)
	g := NewCellGrid(80, 10)
	for _, line := range lines {
		g.AppendInline(line)
	}
	if got := g.RowCount(); got != len(lines) {
		t.Errorf("grid rows = %d, want %d (one row per banner line)", got, len(lines))
	}
	found := false
	for r := 0; r < g.RowCount(); r++ {
		if strings.Contains(g.RowText(r), "|_   _|") {
			found = true
		}
	}
	if !found {
		t.Error("wordmark row missing from the grid")
	}
}

func TestNewWelcomeMessage(t *testing.T) {
	info := welcomeInfo{Tools: 24, Skills: 2, Agents: 6}
	msg := newWelcomeMessage(info)
	if msg.Role != "system" {
		t.Errorf("role = %q, want system", msg.Role)
	}
	if msg.Banner == nil {
		t.Fatal("Banner must be set so the styled renderer is used")
	}
	if *msg.Banner != info {
		t.Errorf("Banner = %+v, want %+v", *msg.Banner, info)
	}
	for _, want := range []string{"24 tools", "2 skills", "6 agents", "/help", "Config:", "Source:"} {
		if !strings.Contains(msg.Content, want) {
			t.Errorf("plain content is missing %q", want)
		}
	}
	// The plain fallback carries no markdown syntax.
	for _, unwanted := range []string{"##", "**", "`"} {
		if strings.Contains(msg.Content, unwanted) {
			t.Errorf("plain content should not contain markdown %q", unwanted)
		}
	}
}

func TestSystemComponentRendersBannerWithoutArrow(t *testing.T) {
	msg := newWelcomeMessage(welcomeInfo{Tools: 1, Skills: 2, Agents: 3})
	chunks := SystemComponent{}.Render(msg, false)
	if len(chunks) != len(renderWelcomeLines(*msg.Banner, 0)) {
		t.Errorf("component rows = %d, want %d", len(chunks), len(renderWelcomeLines(*msg.Banner, 0)))
	}
	for _, c := range chunks {
		if strings.HasPrefix(c.Text, "→ ") {
			t.Errorf("banner row %q must not carry the system arrow", c.Text)
		}
	}
	// Regular system messages keep the arrow.
	plain := SystemComponent{}.Render(chatMessage{Role: "system", Content: "hello"}, false)
	if len(plain) != 1 || plain[0].Text != "→ hello" {
		t.Errorf("regular system message = %+v, want the arrow prefix", plain)
	}
}

func TestWelcomeBannerUsesThemeColors(t *testing.T) {
	defer ApplyTheme(ThemeDefault)

	ApplyTheme(ThemeDefault)
	defaultArt := bannerArtStyle
	if defaultArt.Fg != ThemeDefault.BannerArtFg || !defaultArt.Bold {
		t.Errorf("default banner art style = %+v, want fg %v bold", defaultArt, ThemeDefault.BannerArtFg)
	}
	if bannerKeyStyle.Fg != ThemeDefault.BannerKeyFg {
		t.Errorf("default key style fg = %v, want %v", bannerKeyStyle.Fg, ThemeDefault.BannerKeyFg)
	}

	ApplyTheme(ThemeNord)
	if bannerArtStyle.Fg != ThemeNord.BannerArtFg {
		t.Errorf("nord banner art fg = %v, want %v", bannerArtStyle.Fg, ThemeNord.BannerArtFg)
	}
	if bannerAccentStyle.Fg != ThemeNord.BannerAccentFg {
		t.Errorf("nord accent fg = %v, want %v", bannerAccentStyle.Fg, ThemeNord.BannerAccentFg)
	}
}

// TestWelcomeBannerVisualLayout is a diagnostic aid: run with
// `go test ./tui -run TestWelcomeBannerVisualLayout -v` to inspect the banner.
func TestWelcomeBannerVisualLayout(t *testing.T) {
	for _, width := range []int{100, 70, welcomeMinWidth, 40} {
		t.Logf("--- viewport width %d ---", width)
		for _, line := range renderWelcomeLines(welcomeInfo{Tools: 24, Skills: 2, Agents: 6}, width) {
			t.Logf("|%s|", plainText(line))
		}
	}
}

// TestNewTUIAddsWelcomeBanner checks the startup wiring: the banner replaces
// the old markdown blurb and its counters come from the live registry.
func TestNewTUIAddsWelcomeBanner(t *testing.T) {
	ag := agent.New(nil)
	ag.AddTool(agent.Tool{Name: "noop", Description: "noop"})
	reg := agent.NewRegistry()
	m := NewTUI(ag, &config.Config{}, reg,
		agent.NewProviderRegistry([]agent.ProviderRecord{
			{Name: "test", Provider: &agent.MockProvider{}},
		}), tool.NewTodoStore())

	if len(m.messages) != 1 {
		t.Fatalf("startup messages = %d, want 1", len(m.messages))
	}
	msg := m.messages[0]
	if msg.Role != "system" {
		t.Errorf("welcome role = %q, want system", msg.Role)
	}
	if msg.Banner == nil {
		t.Fatal("welcome message has no Banner, so it renders as plain text")
	}
	if msg.Banner.Tools != 1 {
		t.Errorf("tools = %d, want 1", msg.Banner.Tools)
	}
	if msg.Banner.Agents != len(reg.List()) || msg.Banner.Agents == 0 {
		t.Errorf("agents = %d, want the %d registered agents", msg.Banner.Agents, len(reg.List()))
	}
	if strings.Contains(msg.Content, "##") {
		t.Errorf("welcome content should not be markdown, got %q", msg.Content)
	}
}

// TestWelcomeBannerSourceIsHyperlink checks that the Source row carries an
// OSC 8 target while its visible text stays a plain URL.
func TestWelcomeBannerSourceIsHyperlink(t *testing.T) {
	lines := renderWelcomeLines(welcomeInfo{Tools: 1, Skills: 1, Agents: 1}, 80)

	var linkChunk *CellChunk
	for _, line := range lines {
		for i := range line {
			if strings.Contains(line[i].Text, welcomeSourceURL) {
				linkChunk = &line[i]
			}
		}
	}
	if linkChunk == nil {
		t.Fatalf("banner has no chunk containing %q", welcomeSourceURL)
	}
	if linkChunk.Style.Link != welcomeSourceLink {
		t.Errorf("source link target = %q, want %q", linkChunk.Style.Link, welcomeSourceLink)
	}
	if !strings.HasPrefix(linkChunk.Style.Link, "https://") {
		t.Errorf("link target %q needs a scheme to be clickable", linkChunk.Style.Link)
	}
	if !linkChunk.Style.Underline {
		t.Error("hyperlink should be underlined as a visual cue")
	}
	if got := plainText([]CellChunk{*linkChunk}); got != welcomeSourceURL {
		t.Errorf("hyperlink text = %q, want %q", got, welcomeSourceURL)
	}

	// The Config row is a local path, not a URL.
	for _, line := range lines {
		for _, c := range line {
			if strings.Contains(c.Text, welcomeConfigPath) && c.Style.Link != "" {
				t.Errorf("config path must not be a hyperlink, got %q", c.Style.Link)
			}
		}
	}
}

func TestCellGridEmitsOSC8Hyperlink(t *testing.T) {
	link := CellStyle{Fg: ThemeDefault.BannerArtFg, Underline: true, Link: welcomeSourceLink}
	g := NewCellGrid(80, 3)
	g.AppendInline([]CellChunk{
		{Text: "visit ", Style: DefaultStyle},
		{Text: welcomeSourceURL, Style: link},
		{Text: " now", Style: DefaultStyle},
	})

	out := g.Render()
	if !strings.Contains(out, "\x1b]8;;"+welcomeSourceLink+"\x1b\\") {
		t.Errorf("rendered frame has no OSC 8 opener:\n%q", out)
	}
	if !strings.Contains(out, "\x1b]8;;\x1b\\") {
		t.Errorf("rendered frame has no OSC 8 closer:\n%q", out)
	}
	// Plain-text extraction must stay escape-free.
	if got := g.RowText(0); got != "visit "+welcomeSourceURL+" now" {
		t.Errorf("RowText = %q, want the plain text", got)
	}
	if !strings.Contains(stripANSI(out), "visit "+welcomeSourceURL+" now") {
		t.Errorf("stripped frame lost the link text:\n%s", stripANSI(out))
	}
	// A run without a Link must not emit the sequence.
	plainGrid := NewCellGrid(40, 2)
	plainGrid.AppendInline([]CellChunk{{Text: "no link here", Style: DefaultStyle}})
	if strings.Contains(plainGrid.Render(), "\x1b]8") {
		t.Errorf("plain text must not emit OSC 8: %q", plainGrid.Render())
	}
}

// TestWelcomeBannerThroughView renders the real TUI frame and checks both the
// layout and the per-cell colors of the banner.
func TestWelcomeBannerThroughView(t *testing.T) {
	ApplyTheme(ThemeDefault)
	defer ApplyTheme(ThemeDefault)

	m := &TuiModel{
		ready:  true,
		width:  80,
		height: 40,
		messages: []chatMessage{
			newWelcomeMessage(welcomeInfo{Tools: 24, Skills: 2, Agents: 6}),
		},
		selectStart:  -1,
		selectEnd:    -1,
		charSelStart: selPos{Offset: -1},
		charSelEnd:   selPos{Offset: -1},
		input:        textarea.New(),
		spinner:      spinner.New(),
		status:       StatusIdle,
		sessionStart: time.Now(),
	}
	m.input.SetWidth(80)
	m.vp = viewport.New(80, 30)

	frame := m.View()
	plain := stripANSI(frame)
	for _, want := range []string{"|_   _|", "|___/", "24 tools", "2 skills", "6 agents", "Get started", "/help", "~/.tinycode/config.json"} {
		if !strings.Contains(plain, want) {
			t.Errorf("View() output is missing %q; got:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "→ ##") || strings.Contains(plain, "**24 tools") {
		t.Errorf("View() still renders the raw markdown banner:\n%s", plain)
	}
	// The clickable target must survive the viewport on its way to the screen.
	if !strings.Contains(frame, "\x1b]8;;"+welcomeSourceLink+"\x1b\\") {
		t.Errorf("the Source hyperlink was lost before reaching the terminal:\n%q", frame)
	}
	if strings.Contains(plain, "\x1b]8") {
		t.Errorf("an OSC sequence leaked into the plain-text view:\n%q", plain)
	}

	// The grid cells must carry banner colors (independent of color profile).
	if m.grid == nil {
		t.Fatal("View() did not build a grid")
	}
	artRows, keyRows := 0, 0
	for r := 0; r < m.grid.RowCount(); r++ {
		row := m.grid.RowText(r)
		isArt := strings.Contains(row, "|_   _|")
		isKey := strings.Contains(row, "/help")
		if !isArt && !isKey {
			continue
		}
		for c := 0; c < m.vp.Width; c++ {
			fg := m.grid.Get(r, c).Style.Fg
			if isArt && fg == ThemeDefault.BannerArtFg {
				artRows++
				break
			}
			if isKey && fg == ThemeDefault.BannerKeyFg {
				keyRows++
				break
			}
		}
	}
	if artRows == 0 {
		t.Error("wordmark rows are not painted with the banner art color")
	}
	if keyRows == 0 {
		t.Error("command rows are not painted with the banner key color")
	}
}
