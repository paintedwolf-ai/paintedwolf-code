package session

import (
	"github.com/lycaon/lycaon/internal/project"
)

// SetMutationGate wires lifecycle mutation blocking for runtime work.
func (m *Manager) SetMutationGate(gate *project.MutationGate) {
	if m == nil {
		return
	}
	m.mutationGate = gate
}

func (m *Manager) beginProjectRuntime(projectID string) (func(), error) {
	if m == nil || m.mutationGate == nil {
		return func() {}, nil
	}
	return m.mutationGate.BeginRuntime(projectID)
}
