package worker

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workspace"
)

// SpaceReserveReleaser frees room set aside for finishing a cancel on a full volume.
type SpaceReserveReleaser interface {
	ReleaseSpaceReserve() bool
}

var _ SpaceReserveReleaser = (*workspace.Manager)(nil)

// WorkerRuntimeSettler proves a worker's view and writers are gone before cancellation commits.
type WorkerRuntimeSettler interface {
	SettleBranchRuntime(ctx context.Context, jobID, root string) error
}

// CanceledWorkRetainer keeps a canceled worker's unpromoted bytes before its tree is deleted.
type CanceledWorkRetainer interface {
	RetainCanceledWork(ctx context.Context, jobID, root, baselinePath string, primary projectroot.RootRef) error
}

var _ CanceledWorkRetainer = (*workspace.Manager)(nil)

// retryOnFullDisk gives a cancellation write one more try with the reserve freed. A worker that
// filled the volume the database shares must still be cancelable, or nothing can free the space.
func (b *BranchService) retryOnFullDisk(write func() error) error {
	err := write()
	if !db.IsDiskFull(err) {
		return err
	}
	b.mu.Lock()
	ws := b.workerWorkspace
	b.mu.Unlock()
	if releaser, ok := ws.(SpaceReserveReleaser); ok && releaser.ReleaseSpaceReserve() {
		return write()
	}
	return err
}

// settleCanceledRuntime runs on every cancellation path because each one finishes here. On
// failure the job stays cancel-requested, so maintenance retries it and its data stays put.
func (b *BranchService) settleCanceledRuntime(ctx context.Context, jobID string) error {
	task, ok := b.store.getTask(ctx, jobID)
	if !ok || task.Status.IsTerminal() || strings.TrimSpace(task.WorkspaceRoot) == "" {
		return nil
	}
	b.mu.Lock()
	ws := b.workerWorkspace
	b.mu.Unlock()
	if settler, ok := ws.(WorkerRuntimeSettler); ok {
		if err := settler.SettleBranchRuntime(ctx, jobID, task.WorkspaceRoot); err != nil {
			return err
		}
	}
	if err := workspace.RefuseMountedBranch(task.WorkspaceRoot); err != nil {
		return err
	}
	if retainer, ok := ws.(CanceledWorkRetainer); ok {
		primary := projectroot.RootRef{ID: task.WorkspaceRootID, Path: task.WorkspacePath, IsPrimary: true}
		return retainer.RetainCanceledWork(ctx, jobID, task.WorkspaceRoot, task.WorkspaceBaselinePath, primary)
	}
	return nil
}
