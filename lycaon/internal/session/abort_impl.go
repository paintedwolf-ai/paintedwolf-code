package session

import (
	"github.com/lycaon/lycaon/internal/session/stopping"
)

// SetSessionWorkerAbort wires worker cancellation.
func (m *Manager) SetSessionWorkerAbort(abort stopping.WorkerAbort) {
	if m != nil {
		m.Stops.SetWorkers(abort)
		m.ProjectControl.SetWorkerAbort(abort)
		m.Chats.SetWorkers(abort)
	}
}
