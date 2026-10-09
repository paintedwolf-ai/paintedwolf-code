package session

import (
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
)

// SetLoopWorkflowSource wires workflow run lookups for coordinator loop policy.
func (m *Host) SetLoopWorkflowSource(src *loopwake.WorkflowDomains) {
	if m == nil {
		return
	}
	m.Coordinator.Loop.Workflow = src
	m.Coordinator.Control.Approvals = nil
	m.Coordinator.Control.Obligations = nil
	if src != nil {
		m.Coordinator.Control.Approvals = src.Approvals
		m.Coordinator.Control.Obligations = src.Obligations
	}
	m.Runner.Settlement.SetWorkflowSource(src)
}
