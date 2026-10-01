package tui

import "github.com/charmbracelet/lipgloss"

const crumbSep = " › "

var (
	cursorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	crumbStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	followStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	pausedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	searchStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
)
