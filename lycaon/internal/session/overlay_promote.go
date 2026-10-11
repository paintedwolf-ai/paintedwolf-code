package session

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/promotionstate"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// OverlayWorkspaceChangedPaths returns overlay changes since the leg started.
func OverlayWorkspaceChangedPaths(ctx context.Context, task *api.WorkerTask, roots []projectroot.RootRef) []string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	paths, err := InspectOverlayChanges(ctx, task, roots)
	if err != nil {
		slog.WarnContext(ctx, "workspace baseline changes unavailable", "error", err)
	}
	return paths
}

// InspectOverlayChanges returns overlay changes and any baseline read error.
func InspectOverlayChanges(ctx context.Context, task *api.WorkerTask, roots []projectroot.RootRef) ([]string, error) {
	if task == nil || task.WorkspaceRoot == "" || !OverlayWorkspaceAvailable(task) {
		return nil, nil
	}
	baseline, err := workspacebaseline.Open(ctx, task.WorkspaceBaselinePath, workspacebaseline.ContentStore(task.WorkspaceBaselinePath))
	if err != nil {
		return nil, err
	}
	defer func() { _ = baseline.Close() }()
	changed, err := baseline.Changes(ctx, roots, task.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	if len(roots) > 0 {
		return FilterOverlayPromoteCandidatePathsForRoots(roots, changed), nil
	}
	return promotionstate.FilterOverlayPromoteCandidatePaths(changed), nil
}

// OverlayWorkspaceAvailable reports whether the task's overlay root is a real directory.
func OverlayWorkspaceAvailable(task *api.WorkerTask) bool {
	if task == nil {
		return false
	}
	root := strings.TrimSpace(task.WorkspaceRoot)
	if root == "" {
		return false
	}
	st, err := os.Lstat(root)
	return err == nil && st.IsDir() && st.Mode()&os.ModeSymlink == 0
}

// OverlayAwaitingPromote keeps changed or unreadable write overlays open.
func OverlayAwaitingPromote(ctx context.Context, task *api.WorkerTask) bool {
	if task == nil || !task.EffectiveScope().IsWrite() {
		return false
	}
	if !OverlayWorkspaceAvailable(task) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	paths, err := InspectOverlayChanges(ctx, task, nil)
	return err != nil || len(paths) > 0
}

// OverlayWorkspaceDivergencePaths compares the overlay with the live project.
func OverlayWorkspaceDivergencePaths(task *api.WorkerTask, roots []projectroot.RootRef) []string {
	if task == nil || !task.EffectiveScope().IsWrite() {
		return nil
	}
	overlay := strings.TrimSpace(task.WorkspaceRoot)
	if overlay == "" || !OverlayWorkspaceAvailable(task) {
		return nil
	}
	if len(roots) > 1 {
		projSnap, err := SnapshotWorkspaceFromRoots(roots)
		if err != nil {
			return nil
		}
		changed := DiffBranchWorkspaceSnapshot(roots, overlay, projSnap)
		return FilterOverlayPromoteCandidatePathsForRoots(roots, changed)
	}
	projDir := task.WorkspacePath
	if len(roots) == 1 {
		projDir = roots[0].Path
	}
	projSnap, err := SnapshotWorkspace(projDir)
	if err != nil {
		return nil
	}
	var changed []string
	if len(projSnap) == 0 {
		// Empty live tree: every overlay file is new work to preserve.
		overlaySnap, err := SnapshotWorkspace(overlay)
		if err != nil {
			return nil
		}
		changed = make([]string, 0, len(overlaySnap))
		for rel := range overlaySnap {
			changed = append(changed, rel)
		}
	} else {
		changed = DiffWorkspaceSnapshot(overlay, projSnap)
	}
	return promotionstate.FilterOverlayPromoteCandidatePaths(changed)
}

// ResolveWorkerSummaryStatus keeps completed work open until its overlay is resolved.
func ResolveWorkerSummaryStatus(ctx context.Context, status api.WorkerSummaryStatus, task *api.WorkerTask) api.WorkerSummaryStatus {
	if status != api.WorkerSummaryStatusComplete {
		return status
	}
	if !api.WorkerTaskOverlayOpen(task) && !OverlayAwaitingPromote(ctx, task) {
		return status
	}
	return api.WorkerSummaryStatusOpen
}
