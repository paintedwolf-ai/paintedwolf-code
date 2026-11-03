package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

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

// RecoverOrphanedMergeApplies repairs expired promotion leases.
func (s *MergeService) RecoverOrphanedMergeApplies(ctx context.Context) error {
	if s == nil || s.Store == nil || s.Queue == nil {
		return fmt.Errorf("merge apply sweep requires queue and store")
	}
	eng := s.engine()
	for {
		ids, err := s.Store.ListMergeApplying(ctx)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			task, ok := s.Queue.Get(id)
			if !ok || task == nil || task.MergeStatus != api.WorkerMergeStatusApplying {
				continue
			}
			lock, lockErr := acquirePromoteFileLock(ctx, s.hostDataDirFor(task))
			if lockErr != nil {
				return lockErr
			}
			if _, err := eng.reclaimOrphanedApply(ctx, task, id); err != nil {
				slog.WarnContext(ctx, "orphaned merge apply repair deferred", "job_id", id, "err", err)
			}
			lock.release()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(mergeApplyLease / 3):
		}
	}
}

// mergeApplyClaim holds one promotion lease.
type mergeApplyClaim struct {
	jobID         string
	token         string
	cancel        context.CancelCauseFunc
	heartbeatStop context.CancelFunc
	heartbeatDone chan struct{}
	stopOnce      sync.Once
}

var errMergeApplyClaimLost = errors.New("merge apply claim lost")

var mergeApplyHeartbeatInterval = mergeApplyLease / 3

func (s *MergeService) beginMergeClaim(ctx context.Context, jobID string) (*mergeApplyClaim, context.Context, error) {
	if s == nil || s.Store == nil {
		return nil, nil, fmt.Errorf("overlay promote requires a configured merge store")
	}
	token, claimed, err := s.Store.BeginMergeApply(ctx, jobID)
	if err != nil {
		return nil, nil, err
	}
	if !claimed {
		return nil, nil, s.reject(OverlayPromoteNotPendingCode, map[string]any{"job_id": jobID})
	}
	claimCtx, cancel := context.WithCancelCause(ctx)
	claim := &mergeApplyClaim{jobID: jobID, token: token, cancel: cancel}
	s.engine().startMergeHeartbeat(ctx, claim)
	return claim, claimCtx, nil
}

// startMergeHeartbeat renews the apply lease for the duration of the apply.
func (s mergeEngine) startMergeHeartbeat(ctx context.Context, claim *mergeApplyClaim) {
	hbCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	claim.heartbeatStop = cancel
	claim.heartbeatDone = done
	go func() {
		defer close(done)
		ticker := time.NewTicker(mergeApplyHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				renewed, err := s.Store.RenewMergeApply(hbCtx, claim.jobID, claim.token)
				if err != nil || !renewed {
					slog.WarnContext(hbCtx, "merge apply lease renewal lost", "job_id", claim.jobID, "err", err)
					claim.cancel(errMergeApplyClaimLost)
					return
				}
			}
		}
	}()
}

func (claim *mergeApplyClaim) stop() {
	if claim == nil {
		return
	}
	claim.stopOnce.Do(func() {
		claim.heartbeatStop()
		<-claim.heartbeatDone
		claim.cancel(nil)
	})
}

// releaseMergeClaim returns the branch to pending under the claim.
func (s mergeEngine) releaseMergeClaim(ctx context.Context, claim *mergeApplyClaim) error {
	claim.stop()
	cleanupCtx, cancel := promoteCleanupContext(ctx)
	defer cancel()
	return s.Store.ReleaseMergeApply(cleanupCtx, claim.jobID, claim.token)
}

func (s mergeEngine) teardownBranch(task *api.WorkerTask) error {
	if s.MergeService == nil || s.Workspace == nil || task == nil {
		return nil
	}
	return s.Workspace.DestroyWorkerWorkspace(&workspace.Binding{
		ID:   task.ID,
		Root: task.WorkspaceRoot,
	})
}

// holdBranch leases the overlay's branch tree for a merge read, rebuilding a
// reclaimed tree. A tree that cannot be rebuilt rejects as not pending.
func (s mergeEngine) holdBranch(ctx context.Context, jobID string) (*BranchLease, error) {
	_, lease, err := s.Queue.EnsureWorkerBranch(ctx, jobID)
	if err != nil {
		if errors.Is(err, ErrWorkerBranchUnavailable) {
			return nil, s.reject(OverlayPromoteNotPendingCode, map[string]any{
				"job_id": jobID, "reason": "overlay workspace unavailable",
			})
		}
		return nil, err
	}
	return lease, nil
}
