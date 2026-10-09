package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/heldcall"
)

func (m *Manager) SetBackgroundRegistry(reg *bgprocess.Registry) {
	m.Processes.Background = reg
	m.Runner.Execution.SetProcesses(reg)
	m.Protection.SetJobs(reg)
	_ = m.RegisterSessionCleanup("background-processes", 20, reg.DisposeSession)
}
func (m *Manager) SetHeldCalls(reg *heldcall.Registry) {
	m.Processes.Held = reg
	_ = m.RegisterSessionCleanup("held-calls", 20, func(_ context.Context, id string) error { reg.DisposeSession(id); return nil })
}
