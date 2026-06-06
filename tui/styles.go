package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	colorCyan    = lipgloss.Color("#00FFFF")
	colorYellow  = lipgloss.Color("#FFFF00")
	colorDim     = lipgloss.Color("#888888")
	colorGreen   = lipgloss.Color("#00FF00")
	colorRed     = lipgloss.Color("#FF0000")
	colorWhite   = lipgloss.Color("#FFFFFF")
	colorGray    = lipgloss.Color("#555555")

	// Styles
	headerStyle = lipgloss.NewStyle().
			Foreground(colorCyan).
			Bold(true).
			Padding(0, 1)

	modeStyle = lipgloss.NewStyle().
			Foreground(colorCyan).
			Bold(true)

	thinkingStyle = lipgloss.NewStyle().
			Foreground(colorYellow).
			Faint(true)

	selectedStyle = lipgloss.NewStyle().
			Foreground(colorYellow).
			Background(lipgloss.Color("#333300"))

	userStyle = lipgloss.NewStyle().
			Foreground(colorGreen).
			Bold(true)

	assistantLabelStyle = lipgloss.NewStyle().
				Foreground(colorCyan).
				Bold(true)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorRed)

	footerStyle = lipgloss.NewStyle().
			Foreground(colorGray)

	spinnerStyle = lipgloss.NewStyle().
			Foreground(colorYellow)

	inputPromptStyle = lipgloss.NewStyle().
			Foreground(colorCyan).
			Bold(true)

	dimStyle = lipgloss.NewStyle().
		Foreground(colorGray)
)
