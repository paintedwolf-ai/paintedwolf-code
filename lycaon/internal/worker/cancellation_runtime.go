package worker

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
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
	RetainCanceledWork(ctx context.Context, jobID, root, baselinePath string) error
}

var _ CanceledWorkRetainer = (*workspace.Manager)(nil)

// retryOnFullDisk gives a cancellation write one more try with the reserve freed. A worker that
// filled the volume the database shares must still be cancelable, or nothing can free the space.
func (q *sqlQueueCore) retryOnFullDisk(write func() error) error {
	err := write()
	if !db.IsDiskFull(err) {
		return err
	}
	q.mu.Lock()
	ws := q.workerWorkspace
	q.mu.Unlock()
	if releaser, ok := ws.(SpaceReserveReleaser); ok && releaser.ReleaseSpaceReserve() {
		return write()
	}
	return err
}

// settleCanceledRuntime runs on every cancellation path because each one finishes here. On
// failure the job stays cancel-requested, so maintenance retries it and its data stays put.
func (q *sqlQueueCore) settleCanceledRuntime(ctx context.Context, jobID string) error {
	task, ok := q.Get(jobID) //nolint:contextcheck // Get has internal sqlQueueGetTimeout
	if !ok || task.Status.IsTerminal() || strings.TrimSpace(task.WorkspaceRoot) == "" {
		return nil
	}
	q.mu.Lock()
	ws := q.workerWorkspace
	q.mu.Unlock()
	if settler, ok := ws.(WorkerRuntimeSettler); ok {
		if err := settler.SettleBranchRuntime(ctx, jobID, task.WorkspaceRoot); err != nil {
			return err
		}
	}
	if err := workspace.RefuseMountedBranch(task.WorkspaceRoot); err != nil {
		return err
	}
	if retainer, ok := ws.(CanceledWorkRetainer); ok {
		return retainer.RetainCanceledWork(ctx, jobID, task.WorkspaceRoot, task.WorkspaceBaselinePath)
	}
	return nil
}
