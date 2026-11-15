package board

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// MaxBoardWorkersPage bounds one pack_board worker page.
const MaxBoardWorkersPage = 20

// PlanWorkersFunc returns the worker names in sessionID bound to a Blueprint path.
type PlanWorkersFunc func(ctx context.Context, sessionID, plan string) (map[string]bool, error)

// ToolDeps holds dependencies for board orientation tools.
type ToolDeps struct {
	Builder            *SnapshotBuilder
	PlanWorkers        PlanWorkersFunc
	Findings           func() findings.Store
	RootSession        func(context.Context, string) string
	PromotePaths       func(sessionID string) []api.WorkerPromoteJobPathStatus
	OverlayMergePlan   func(ctx context.Context, sessionID string, tasks []api.WorkerTask) *api.OverlayMergePlan
	ActiveReservations func(sessionID string) []api.BoardReservationEntry
}

// RegisterBoardTools registers pack_board and read_findings on the tool registry.
func RegisterBoardTools(reg *tools.DefaultRegistry, deps ToolDeps) error {
	if reg == nil || deps.Builder == nil {
		return fmt.Errorf("registry and snapshot builder required")
	}
	if err := reg.Register("read_findings", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		return readFindings(ctx, args, tctx, deps)
	}); err != nil {
		return err
	}
	if err := reg.Register("pack_board", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if args["finding_id"] != nil || args["findings_after"] != nil {
			field := "finding_id"
			if args["finding_id"] == nil {
				field = "findings_after"
			}
			return "", &toolrejection.ToolReject{
				Code: "TOOL_ARGS_INVALID",
				Data: map[string]any{
					"tool":             "pack_board",
					"field":            field,
					"reason":           "moved_to_read_findings",
					"replacement_tool": "read_findings",
				},
			}
		}
		offset, err := parseBoardWorkerCursor(args["cursor"])
		if err != nil {
			return "", err
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
		snap, err := deps.Builder.Build(ctx, tctx.Identity.ProjectID, tctx.ActiveRootPath(), sessionID, level, tctx.Source.Roots)
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
		// Path rows stay on the board Den shows; reserved paths reach the
		// coordinator in the board text.
		view.PromotePaths = nil
		for i := range view.Workers {
			view.Workers[i].Reservations = nil
		}
		if planFilter, _ := args["plan"].(string); strings.TrimSpace(planFilter) != "" && deps.PlanWorkers != nil {
			matched, pErr := deps.PlanWorkers(ctx, sessionID, strings.TrimSpace(planFilter))
			if pErr != nil {
				return "", pErr
			}
			filtered := make([]api.BoardWorker, 0, len(view.Workers))
			for _, w := range view.Workers {
				if matched[w.Name] {
					filtered = append(filtered, w)
				}
			}
			view.Workers = filtered
		}
		pageBoardWorkers(&view, offset)
		if deps.OverlayMergePlan != nil {
			tasks := packboard.WorkerTasksFromSnapshot(*snap)
			if plan := deps.OverlayMergePlan(ctx, sessionID, tasks); plan != nil && plan.PendingCount > 0 {
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

func parseBoardWorkerCursor(raw any) (int, error) {
	if raw == nil {
		return 0, nil
	}
	s, ok := raw.(string)
	if !ok {
		return 0, &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "pack_board", "field": "cursor", "reason": "invalid_cursor"}}
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "pack_board", "field": "cursor", "reason": "invalid_cursor"}}
	}
	return n, nil
}

func pageBoardWorkers(view *api.BoardView, offset int) {
	total := len(view.Workers)
	if total == 0 {
		return
	}
	if offset >= total {
		view.Workers = []api.BoardWorker{}
		return
	}
	end := offset + MaxBoardWorkersPage
	if end < total {
		view.WorkersHasMore = true
		view.WorkersOmitted = total - end
		view.WorkersNextCursor = strconv.Itoa(end)
		view.Workers = view.Workers[offset:end]
		return
	}
	view.Workers = view.Workers[offset:]
}
