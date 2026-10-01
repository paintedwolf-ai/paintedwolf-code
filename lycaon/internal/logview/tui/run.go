package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	logview "github.com/lycaon/lycaon/internal/logview"
)

// Run launches the interactive browser. An empty dir opens the picker listing all
// captures; a non-empty dir opens that capture directly at its session view.
func Run(cfg logview.Config, dir string) error {
	m, err := New(cfg, dir)
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
