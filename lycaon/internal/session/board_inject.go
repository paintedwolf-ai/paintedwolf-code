package session

import (
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
)

// SetBoardInject wires proactive pack board inject on coordinator Prompt.
func (m *Manager) SetBoardInject(builder assembly.BoardSnapshotBuilder, formatter assembly.BoardPackFormatter) {
	if m == nil {
		return
	}
	m.boardBuilder = builder
	m.boardFormatter = formatter
	rt := m.ensureCoordinatorRuntime()
	rt.SetBoardInject(builder, formatter, func() bool {
		return m.coordinatorFrame != nil
	})
	rt.Board().SetWorkerRoots(m.Workers.Workspaces.BoardRoots)
	rt.SetPromotePathOverlay(m.Promotion.PromotePathBoardLines)
	rt.SetOverlayMergePlan(m.OverlayMergePlanFn())
	if m.includeScanLegend != nil {
		rt.SetIncludeScanLegend(m.includeScanLegend)
	}
}

// SetIncludeScanLegend configures whether scan legend is included in board inject.
func (m *Manager) SetIncludeScanLegend(fn func() bool) {
	if m == nil {
		return
	}
	m.includeScanLegend = fn
	if m.coordinatorRuntime != nil {
		m.coordinatorRuntime.SetIncludeScanLegend(fn)
	}
}
