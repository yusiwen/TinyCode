package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Styles (initialized by ApplyTheme on startup)
	headerStyle    lipgloss.Style
	selectedStyle  lipgloss.Style
	statusBarStyle lipgloss.Style
	spinnerStyle   lipgloss.Style
	dimStyle       lipgloss.Style
)
