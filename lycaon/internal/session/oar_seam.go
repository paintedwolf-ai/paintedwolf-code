package session

import (
	"github.com/lycaon/lycaon/internal/oar"
)

// SetOARPipeline connects policy evaluation, rendering, and advisory delivery.
func (m *Host) SetOARPipeline(p *oar.GuardPipeline, r *oar.Renderer) {
	if m == nil {
		return
	}
	m.Resources.Tools.Counters = nil
	if p != nil {
		m.Resources.Tools.Counters = p.Counters()
	}
	m.Workers.Delivery.SetPipeline(p)
	m.ToolPolicy.SetPipeline(p)
	m.Workers.Summaries.SetEvaluation(m.Workers.WorkspaceCheck, m.Coordinator.Completion.Hints, m.ToolPolicy.Pipeline)
	m.Workers.SetEvaluation(m.Workers.WorkspaceCheck, m.Coordinator.Completion.Hints, m.ToolPolicy.Pipeline)
	m.Coordinator.Feedback.SetRenderer(r)
	m.Coordinator.Guidance.SetRenderer(r)
	if p != nil {
		p.SetAdvisorySink(m.Coordinator.Guidance.DeliverAdvisories)
	}
}
