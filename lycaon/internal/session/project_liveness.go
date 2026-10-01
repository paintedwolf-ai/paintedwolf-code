package session

import (
	"github.com/lycaon/lycaon/internal/projectliveness"
)

// SetProjectLiveness wires tracker for project parking and active turn tracking.
func (m *Manager) SetProjectLiveness(tracker *projectliveness.Tracker) {
	if m == nil {
		return
	}
	m.projectLiveness = tracker
}

func (m *Manager) claimTurnLiveness(projectID, turnID string) func() {
	if m == nil || m.projectLiveness == nil {
		return func() {}
	}
	return m.projectLiveness.ClaimTurn(projectID, turnID)
}
