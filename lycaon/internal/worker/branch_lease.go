package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrWorkerBranchUnavailable reports a branch tree that is gone and cannot be
// rebuilt: the job has no sealed overlay to rebuild it from.
var ErrWorkerBranchUnavailable = errors.New("worker branch is unavailable")

// BranchLease is a held worker branch. While it is held the tree stays on
// disk; retention steps aside. Release it when the read is over.
type BranchLease struct {
	Root    string
	release func()
}

// Release ends the lease. It is safe to call more than once.
func (l *BranchLease) Release() {
	if l == nil || l.release == nil {
		return
	}
	l.release()
	l.release = nil
}

// WorkerBranches materializes a job's branch tree on demand. A tree that
// retention reclaimed is rebuilt from the job's baseline and overlay records
// before the lease is handed back, so callers never see an evicted branch.
type WorkerBranches interface {
	EnsureWorkerBranch(ctx context.Context, jobID string) (*api.WorkerTask, *BranchLease, error)
}

// ensureWorkerBranch is the shared lease path for both queue implementations.
func ensureWorkerBranch(ctx context.Context, task *api.WorkerTask, ws WorkerWorkspaceManager) (*BranchLease, error) {
	if task == nil || !task.EffectiveScope().IsWrite() {
		return nil, fmt.Errorf("%w: %s is not a write worker", ErrWorkerBranchUnavailable, taskIDOrBlank(task))
	}
	root := strings.TrimSpace(task.WorkspaceRoot)
	if root == "" {
		return nil, fmt.Errorf("%w: %s has no branch", ErrWorkerBranchUnavailable, task.ID)
	}
	release, err := workspace.AcquireBranchUse(ctx, root)
	if err != nil {
		return nil, err
	}
	if workspace.BranchTreePresent(root) {
		workspace.TouchBranchUse(root)
		return &BranchLease{Root: root, release: release}, nil
	}
	if strings.TrimSpace(task.WorkspaceOverlayPath) == "" || ws == nil {
		release()
		return nil, fmt.Errorf("%w: %s branch tree is missing and its overlay was never sealed", ErrWorkerBranchUnavailable, task.ID)
	}
	_, err = ws.RebuildWorkerWorkspace(ctx, root, func(ctx context.Context, layout workspace.SandboxLayout, branchRoot string) error {
		report, err := workspacebaseline.Materialize(ctx, task.WorkspaceBaselinePath, task.WorkspaceOverlayPath, workspacebaseline.ContentStore(task.WorkspaceBaselinePath), layout.Roots, branchRoot, currentStandIn(layout.Roots))
		if err != nil {
			return err
		}
		workerWorkspaceLog.Info("rebuilt worker branch", "job_id", task.ID, "files", report.Files,
			"bytes", report.Bytes, "deleted", report.Deleted, "unavailable", len(report.Unavailable))
		return nil
	})
	if err != nil {
		release()
		return nil, fmt.Errorf("rebuild worker branch %s: %w", task.ID, err)
	}
	return &BranchLease{Root: root, release: release}, nil
}

// currentStandIn names the project file that stands in for an opaque baseline
// body, or "" when the project has no regular file there.
func currentStandIn(roots []projectroot.RootRef) func(path string) string {
	return func(path string) string {
		abs, err := workspacebaseline.CanonicalPath(roots, path)
		if err != nil {
			return ""
		}
		info, err := os.Lstat(abs)
		if err != nil || !info.Mode().IsRegular() {
			return ""
		}
		return abs
	}
}

func taskIDOrBlank(task *api.WorkerTask) string {
	if task == nil {
		return ""
	}
	return task.ID
}

// LeaseExistingBranch holds a branch tree that is already on disk. It never
// rebuilds; callers that own a queue use EnsureWorkerBranch instead.
func LeaseExistingBranch(ctx context.Context, task *api.WorkerTask) (*BranchLease, error) {
	return ensureWorkerBranch(ctx, task, nil)
}

// BranchJob is the retention-relevant state of one job that names a branch.
type BranchJob struct {
	ID        string
	ProjectID string
	// Sealed reports a completed overlay whose changes are on record, so its
	// tree can be reclaimed and rebuilt.
	Sealed bool
}

func branchJobSealed(status api.WorkerStatus, mergeStatus api.WorkerMergeStatus, overlayPath string) bool {
	if strings.TrimSpace(overlayPath) == "" || status != api.WorkerStatusComplete {
		return false
	}
	switch mergeStatus {
	case api.WorkerMergeStatusPending, api.WorkerMergeStatusRebasing:
		return true
	case api.WorkerMergeStatusApplying, api.WorkerMergeStatusMerged, api.WorkerMergeStatusOrphaned,
		api.WorkerMergeStatusRejected, api.WorkerMergeStatusAborted:
	}
	return false
}
