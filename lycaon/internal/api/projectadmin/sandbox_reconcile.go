package projectadmin

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	sandboxReconcileDebounce = 30 * time.Second
	sandboxReconcileTimeout  = 2 * time.Minute
)

func (s *Sandboxes) ScheduleProjectSandboxReconcile(projectID, workspacePath string) {
	if s == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	workspacePath = strings.TrimSpace(workspacePath)
	if projectID == "" || workspacePath == "" {
		return
	}
	reconcileKey := projectID + "\x00" + workspacePath
	s.sandboxReconcileMu.Lock()
	if s.sandboxReconcileLast == nil {
		s.sandboxReconcileLast = make(map[string]time.Time)
	}
	if s.sandboxReconcilePending == nil {
		s.sandboxReconcilePending = make(map[string]struct{})
	}
	if last, ok := s.sandboxReconcileLast[reconcileKey]; ok && time.Since(last) < sandboxReconcileDebounce {
		s.sandboxReconcileMu.Unlock()
		return
	}
	if _, running := s.sandboxReconcilePending[reconcileKey]; running {
		s.sandboxReconcileMu.Unlock()
		return
	}
	s.sandboxReconcilePending[reconcileKey] = struct{}{}
	s.sandboxReconcileMu.Unlock()

	s.background.Go(context.Background(), func(ctx context.Context) {
		defer func() {
			s.sandboxReconcileMu.Lock()
			delete(s.sandboxReconcilePending, reconcileKey)
			s.sandboxReconcileLast[reconcileKey] = time.Now()
			s.sandboxReconcileMu.Unlock()
		}()
		reconcileCtx, cancel := context.WithTimeout(ctx, sandboxReconcileTimeout)
		defer cancel()
		if removed := s.reconcileProjectSandboxes(reconcileCtx, projectID, workspacePath); removed > 0 {
			slog.InfoContext(ctx, "background sandbox reconcile", "project_id", projectID, "workspace_path", workspacePath, "removed", removed)
		}
	})
}

func (s *Sandboxes) reconcileProjectSandboxes(ctx context.Context, projectID, workspacePath string) int {
	if s.Workers == nil {
		return 0
	}
	projectID = strings.TrimSpace(projectID)
	workspacePath = strings.TrimSpace(workspacePath)
	if projectID == "" || workspacePath == "" {
		return 0
	}
	removed, err := worker.ReconcileProjectSandboxes(ctx, s.WorkerBranchRoot, workspacePath, func(ctx context.Context) ([]wire.WorkerTask, error) {
		return s.Workers.ListByWorkspacePath(ctx, workspacePath)
	})
	if err != nil {
		slog.WarnContext(ctx, "reconcile worker sandboxes", "project_id", projectID, "workspace_path", workspacePath, "err", err)
		return removed
	}
	if removed > 0 && s.Board != nil && s.Board.Repo != nil {
		s.Board.Repo.Changed(ctx, workspacePath)
	}
	if removedCheckpoints := s.Sessions.RemoveOrphanCheckpoints(ctx, workspacePath); removedCheckpoints > 0 {
		slog.InfoContext(ctx, "removed orphan session checkpoints", "workspace_path", workspacePath, "removed", removedCheckpoints)
	}
	return removed
}
