package promotionstate

import (
	"github.com/lycaon/lycaon/internal/overlayplan"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) MergePlan() func(sessionID string, tasks []api.WorkerTask) *api.OverlayMergePlan {
	return func(sessionID string, tasks []api.WorkerTask) *api.OverlayMergePlan {
		plan := overlayplan.Build(sessionID, tasks, func(sid, jobID string) (overlayplan.PreviewSnapshot, bool) {
			order, after, blocked, clean, conflict, ok := m.OverlayPreviewSnapshot(sid, jobID)
			if !ok {
				return overlayplan.PreviewSnapshot{}, false
			}
			return overlayplan.PreviewSnapshot{
				PromoteOrder:  order,
				PromoteAfter:  after,
				BlockedBy:     blocked,
				CleanPaths:    clean,
				ConflictPaths: conflict,
			}, true
		})
		if plan.PendingCount == 0 {
			return nil
		}
		cp := plan
		return &cp
	}
}
