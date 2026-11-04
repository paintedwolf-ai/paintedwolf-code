package session

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/promotionstate"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// TaskCheckpointStoreSubdir is the conventional checkpoint store subdirectory for a worker task.
const TaskCheckpointStoreSubdir = ".lycaon-checkpoint-store"

// CheckpointStoreDirForTask resolves the durable checkpoint store directory for task if one exists.
func CheckpointStoreDirForTask(task *api.WorkerTask) string {
	if task == nil {
		return ""
	}
	for _, raw := range []string{task.WorkspaceBaselinePath, task.WorkspaceOverlayPath} {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		for _, cand := range []string{p, filepath.Join(p, TaskCheckpointStoreSubdir), filepath.Dir(p), filepath.Join(filepath.Dir(p), TaskCheckpointStoreSubdir)} {
			if isCheckpointStoreDir(cand) {
				return cand
			}
		}
	}
	if root := strings.TrimSpace(task.WorkspaceRoot); root != "" {
		for _, cand := range []string{filepath.Join(root, TaskCheckpointStoreSubdir), root + TaskCheckpointStoreSubdir} {
			if isCheckpointStoreDir(cand) {
				return cand
			}
		}
	}
	return ""
}

func isCheckpointStoreDir(dir string) bool {
	if dir == "" || dir == "." || dir == "/" {
		return false
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return false
	}
	cpSt, err := os.Stat(filepath.Join(dir, "checkpoints"))
	if err != nil || !cpSt.IsDir() {
		return false
	}
	blobSt, err := os.Stat(filepath.Join(dir, "blobs"))
	return err == nil && blobSt.IsDir()
}

// OpenTaskCheckpointStore loads the reviewed checkpoint, or latest when no preview is bound.
func OpenTaskCheckpointStore(task *api.WorkerTask) (*checkpoint.Store, checkpoint.Manifest, bool, error) {
	dir := CheckpointStoreDirForTask(task)
	if dir == "" {
		return nil, checkpoint.Manifest{}, false, nil
	}
	store, err := checkpoint.NewStore(dir)
	if err != nil {
		return nil, checkpoint.Manifest{}, true, err
	}
	checkpointID := boundTaskCheckpointID(task.ID)
	var manifest checkpoint.Manifest
	if checkpointID == "" {
		manifest, err = store.LoadLatestManifest()
	} else {
		manifest, err = store.LoadManifest(checkpointID)
	}
	if err != nil {
		if errors.Is(err, checkpoint.ErrCheckpointNotFound) && checkpointID == "" {
			return store, checkpoint.Manifest{}, false, nil
		}
		return nil, checkpoint.Manifest{}, true, err
	}
	return store, manifest, true, nil
}

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

// InspectOverlayChanges returns overlay changes and any baseline or checkpoint read error.
func InspectOverlayChanges(ctx context.Context, task *api.WorkerTask, roots []projectroot.RootRef) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if task == nil || !OverlayWorkspaceAvailable(task) {
		return nil, nil
	}
	if _, manifest, hasCheckpoint, err := OpenTaskCheckpointStore(task); err != nil {
		return nil, err
	} else if hasCheckpoint {
		return checkpointManifestChangedPaths(manifest, roots), nil
	}
	if task.WorkspaceRoot == "" {
		return nil, nil
	}
	if paths, handled, err := workspace.OverlayWorkspaceDivergencePaths(ctx, task.WorkspaceRoot); handled {
		if err != nil {
			return nil, err
		}
		if len(roots) > 0 {
			return FilterOverlayPromoteCandidatePathsForRoots(roots, paths), nil
		}
		return promotionstate.FilterOverlayPromoteCandidatePaths(paths), nil
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

// OverlayWorkspaceAvailable reports whether the task's overlay root is a real directory or has a published checkpoint store.
func OverlayWorkspaceAvailable(task *api.WorkerTask) bool {
	if task == nil {
		return false
	}
	root := strings.TrimSpace(task.WorkspaceRoot)
	if root != "" {
		st, err := os.Lstat(root)
		if err != nil {
			return CheckpointStoreDirForTask(task) != ""
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return false
		}
		return true
	}
	return CheckpointStoreDirForTask(task) != ""
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

// OverlayWorkspaceDivergencePaths compares the overlay or published checkpoint with the live project.
func OverlayWorkspaceDivergencePaths(task *api.WorkerTask, roots []projectroot.RootRef) []string {
	if task == nil || !task.EffectiveScope().IsWrite() {
		return nil
	}
	if _, manifest, hasCheckpoint, err := OpenTaskCheckpointStore(task); err == nil && hasCheckpoint {
		return checkpointManifestChangedPaths(manifest, roots)
	}
	overlay := strings.TrimSpace(task.WorkspaceRoot)
	if overlay == "" || !OverlayWorkspaceAvailable(task) {
		return nil
	}
	if paths, handled, err := workspace.OverlayWorkspaceDivergencePaths(context.Background(), overlay); handled && err == nil {
		if len(roots) > 1 {
			return FilterOverlayPromoteCandidatePathsForRoots(roots, paths)
		}
		return promotionstate.FilterOverlayPromoteCandidatePaths(paths)
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

// QualifyCheckpointPath formats a checkpoint path as a qualified promote path across multi-root scopes.
func QualifyCheckpointPath(qpath layeredfs.QualifiedPath, roots []projectroot.RootRef) string {
	rel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(qpath.Rel)))
	if rel == "" || rel == "." {
		return ""
	}
	if len(roots) > 1 {
		if primary, err := projectroot.PrimaryRoot(roots); err == nil {
			for _, r := range roots {
				if r.ID == qpath.RootID || ((qpath.RootID == "primary" || qpath.RootID == "root-0" || qpath.RootID == "") && r.IsPrimary) {
					return projectroot.Qualify(primary, r, filepath.Join(r.Path, filepath.FromSlash(rel)))
				}
			}
		}
	}
	return rel
}

func checkpointManifestChangedPaths(manifest checkpoint.Manifest, roots []projectroot.RootRef) []string {
	changed := make([]string, 0, len(manifest.Entries))
	for _, e := range manifest.Entries {
		if rel := QualifyCheckpointPath(e.Path, roots); rel != "" {
			changed = append(changed, rel)
		}
	}
	if len(roots) > 0 {
		return FilterOverlayPromoteCandidatePathsForRoots(roots, changed)
	}
	return promotionstate.FilterOverlayPromoteCandidatePaths(changed)
}
