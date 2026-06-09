package tui

import "github.com/charmbracelet/lipgloss"

// Theme defines a complete color scheme for the TUI.
type Theme struct {
	Name string

	// --- CellGrid colors ---
	DefaultFg      lipgloss.Color
	SelectionBg    lipgloss.Color
	SelectionFg    lipgloss.Color
	DimFg          lipgloss.Color
	ThinkingFg     lipgloss.Color
	ResponseFg     lipgloss.Color
	UserFg         lipgloss.Color
	SystemFg       lipgloss.Color
	HeadingFg      lipgloss.Color
	InlineCodeFg   lipgloss.Color
	StatusBarFg    lipgloss.Color

	// --- Lipgloss TUI colors ---
	HeaderFg       lipgloss.Color
	ModeFg         lipgloss.Color
	ErrorFg        lipgloss.Color
	SuccessFg      lipgloss.Color
	WarningFg      lipgloss.Color
	SpinnerFg      lipgloss.Color
	StatusBarBg    lipgloss.Color
	InputFg        lipgloss.Color
	InputBg        lipgloss.Color
	PlaceholderFg  lipgloss.Color
}

var (
	ThemeDefault = Theme{
		Name: "default",

		DefaultFg:      lipgloss.Color("#E8E8E8"),
		SelectionBg:    lipgloss.Color("#333300"),
		SelectionFg:    lipgloss.Color("#FFD700"),
		DimFg:          lipgloss.Color("#888888"),
		ThinkingFg:     lipgloss.Color("#FFB347"),
		ResponseFg:     lipgloss.Color("#FFD700"),
		UserFg:         lipgloss.Color("#00FF00"),
		SystemFg:       lipgloss.Color("#888888"),
		HeadingFg:      lipgloss.Color("#E8E8E8"),
		InlineCodeFg:   lipgloss.Color("#FDD700"),
		StatusBarFg:    lipgloss.Color("#AAAAAA"),

		HeaderFg:       lipgloss.Color("#00FFFF"),
		ModeFg:         lipgloss.Color("#00FFFF"),
		ErrorFg:        lipgloss.Color("#FF0000"),
		SuccessFg:      lipgloss.Color("#00FF00"),
		WarningFg:      lipgloss.Color("#FFA500"),
		SpinnerFg:      lipgloss.Color("#FFFF00"),
		StatusBarBg:    lipgloss.Color("#000000"),
		InputFg:        lipgloss.Color("#FFFFFF"),
		InputBg:        lipgloss.Color("#000000"),
		PlaceholderFg:  lipgloss.Color("#555555"),
	}

	ThemeNord = Theme{
		Name: "nord",

		DefaultFg:      lipgloss.Color("#D8DEE9"),
		SelectionBg:    lipgloss.Color("#434C5E"),
		SelectionFg:    lipgloss.Color("#EBCB8B"),
		DimFg:          lipgloss.Color("#4C566A"),
		ThinkingFg:     lipgloss.Color("#EBCB8B"),
		ResponseFg:     lipgloss.Color("#88C0D0"),
		UserFg:         lipgloss.Color("#A3BE8C"),
		SystemFg:       lipgloss.Color("#81A1C1"),
		HeadingFg:      lipgloss.Color("#81A1C1"),
		InlineCodeFg:   lipgloss.Color("#EBCB8B"),
		StatusBarFg:    lipgloss.Color("#D8DEE9"),

		HeaderFg:       lipgloss.Color("#88C0D0"),
		ModeFg:         lipgloss.Color("#88C0D0"),
		ErrorFg:        lipgloss.Color("#BF616A"),
		SuccessFg:      lipgloss.Color("#A3BE8C"),
		WarningFg:      lipgloss.Color("#D08770"),
		SpinnerFg:      lipgloss.Color("#88C0D0"),
		StatusBarBg:    lipgloss.Color("#3B4252"),
		InputFg:        lipgloss.Color("#D8DEE9"),
		InputBg:        lipgloss.Color("#3B4252"),
		PlaceholderFg:  lipgloss.Color("#4C566A"),
	}
)

var (
	currentTheme = ThemeDefault

	themes = map[string]Theme{
		"default": ThemeDefault,
		"nord":    ThemeNord,
	}
)

func init() {
	ApplyTheme(ThemeDefault)
}

// ApplyTheme updates all global styles to the given theme.
func ApplyTheme(t Theme) {
	currentTheme = t

	// CellGrid styles
	DefaultStyle     = CellStyle{}
	ThinkingStyle    = CellStyle{Fg: t.ThinkingFg}
	ResponseLabel   = CellStyle{Fg: t.ResponseFg, Bold: true}
	HeadingStyle     = CellStyle{Fg: t.HeadingFg, Bold: true}
	DimStyle         = CellStyle{Fg: t.DimFg}
	SelectionStyle   = CellStyle{Fg: t.SelectionFg, Bg: t.SelectionBg}
	UserStyle        = CellStyle{Fg: t.UserFg, Bold: true}
	CodeStyle        = CellStyle{Fg: t.InlineCodeFg}
	SystemStyle      = CellStyle{Fg: t.SystemFg}
	StatusBarStyle   = CellStyle{Fg: t.StatusBarFg}

	// Lipgloss styles
	headerStyle = lipgloss.NewStyle().
			Foreground(t.HeaderFg).
			Bold(true).
			Padding(0, 1)

	modeStyle = lipgloss.NewStyle().
			Foreground(t.ModeFg).
			Bold(true)

	thinkingStyle = lipgloss.NewStyle().
			Foreground(t.ThinkingFg).
			Faint(true)

	selectedStyle = lipgloss.NewStyle().
			Foreground(t.SelectionFg).
			Background(t.SelectionBg)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(t.StatusBarFg).
			Background(t.StatusBarBg)

	spinnerStyle = lipgloss.NewStyle().
			Foreground(t.SpinnerFg)

	userStyle = lipgloss.NewStyle().
			Foreground(t.UserFg).
			Bold(true)

	assistantLabelStyle = lipgloss.NewStyle().
				Foreground(t.ResponseFg).
				Bold(true)

	errorStyle = lipgloss.NewStyle().
			Foreground(t.ErrorFg)

	footerStyle = lipgloss.NewStyle().
			Foreground(t.DimFg)

	inputPromptStyle = lipgloss.NewStyle().
			Foreground(t.ModeFg).
			Bold(true)

	dimStyle = lipgloss.NewStyle().
			Foreground(t.DimFg)

	// Clear style cache so next render picks up new colors
	styleMu.Lock()
	styleCache = make(map[CellStyle]lipgloss.Style)
	styleMu.Unlock()
}

// ThemeNames returns all available theme names.
func ThemeNames() []string {
	names := make([]string, 0, len(themes))
	for n := range themes {
		names = append(names, n)
	}
	return names
}

// LookupTheme finds a theme by name.
func LookupTheme(name string) *Theme {
	t, ok := themes[name]
	if !ok {
		return nil
	}
	return &t
}
