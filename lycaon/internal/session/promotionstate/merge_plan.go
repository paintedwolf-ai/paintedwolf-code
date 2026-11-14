package promotionstate

import (
	"context"
	"log/slog"

	"github.com/lycaon/lycaon/internal/overlayplan"
	"github.com/lycaon/lycaon/pkg/api"
)

// LatestResults maps the job of each worker's latest result to its names.
type LatestResults func(ctx context.Context, sessionID string) (map[string]overlayplan.LatestResult, error)

// SetLatestResults names the results the merge plan covers.
func (m *Service) SetLatestResults(fn LatestResults) { m.latestResults = fn }

// MergePlan plans the landing of each worker's latest pending result.
func (m *Service) MergePlan() func(ctx context.Context, sessionID string, tasks []api.WorkerTask) *api.OverlayMergePlan {
	return func(ctx context.Context, sessionID string, tasks []api.WorkerTask) *api.OverlayMergePlan {
		if m.latestResults == nil {
			return nil
		}
		latest, err := m.latestResults(ctx, sessionID)
		if err != nil {
			slog.WarnContext(ctx, "name results for the merge plan", "session_id", sessionID, "error", err)
			return nil
		}
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
		}, latest)
		if plan.PendingCount == 0 {
			return nil
		}
		cp := plan
		return &cp
	}
}
