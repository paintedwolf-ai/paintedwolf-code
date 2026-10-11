package coordinatorcontrol

import (
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/session/promotionstate"
)

// ConfigureBoard binds shared source and worker projections to the board resource.
func (m *Service) ConfigureBoard(builder assembly.BoardSnapshotBuilder, formatter assembly.BoardPackFormatter, promotion *promotionstate.Service) {
	rt, context, workspaces := m.Runtime, m.Context, m.Context.Workspaces
	rt.SetBoardInject(builder, formatter, func() bool { return context.Frame != nil })
	rt.Board().SetWorkerRoots(workspaces.BoardRoots)
	rt.SetActiveReservations(workspaces.ReservationEntries)
	rt.SetPromotePathOverlay(promotion.PromotePathBoardLines)
	rt.SetOverlayMergePlan(promotion.MergePlan())
}
