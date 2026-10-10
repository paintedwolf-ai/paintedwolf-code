package board

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/findings"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// ToolDeps holds dependencies for board orientation tools.
type ToolDeps struct {
	ReviewView         func(context.Context, map[string]any, tools.ToolContext) (string, error)
	Builder            *SnapshotBuilder
	Findings           func() findings.Store
	RootSession        func(context.Context, string) string
	PromotePaths       func(sessionID string) []api.WorkerPromoteJobPathStatus
	OverlayMergePlan   func(sessionID string, tasks []api.WorkerTask) *api.OverlayMergePlan
	ActiveReservations func(sessionID string) []api.BoardReservationEntry
}

// RegisterBoardTools registers pack_board on the tool registry.
func RegisterBoardTools(reg *tools.DefaultRegistry, deps ToolDeps) error {
	if reg == nil || deps.Builder == nil {
		return fmt.Errorf("registry and snapshot builder required")
	}
	if err := reg.Register("pack_board", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if args["review_view"] != nil {
			if deps.ReviewView == nil {
				return "", fmt.Errorf("review view unavailable")
			}
			return deps.ReviewView(ctx, args, tctx)
		}
		if args["finding_id"] != nil || args["findings_after"] != nil {
			return readFindings(ctx, args, tctx, deps)
		}
		if strings.TrimSpace(tctx.ActiveRootPath()) == "" {
			return "", fmt.Errorf("project_dir required")
		}
		level := api.BoardDetailLevelCompact
		if raw, ok := args["detail_level"].(string); ok && strings.TrimSpace(raw) != "" {
			level = api.BoardDetailLevel(strings.TrimSpace(raw))
		}
		sessionID := CoordinatorBoardSessionID(tctx)
		if sessionID == "" {
			return "", fmt.Errorf("session_id required")
		}
		snap, err := deps.Builder.Build(ctx, tctx.ProjectID, tctx.ActiveRootPath(), sessionID, level, tctx.Roots)
		if err != nil {
			return "", err
		}
		if deps.PromotePaths != nil {
			packboard.EnrichSnapshotPromotePaths(snap, deps.PromotePaths(sessionID))
		}
		if deps.ActiveReservations != nil {
			packboard.EnrichSnapshotReservationsForSession(sessionID, snap, deps.ActiveReservations)
		}
		view := BuildBoardView(snap, sessionID, level, time.Now().UTC())
		if deps.OverlayMergePlan != nil {
			tasks := packboard.WorkerTasksFromSnapshot(*snap)
			if plan := deps.OverlayMergePlan(sessionID, tasks); plan != nil && plan.PendingCount > 0 {
				view.OverlayMergePlan = plan
			}
		}
		raw, err := json.Marshal(view)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}); err != nil {
		return err
	}
	return nil
}
