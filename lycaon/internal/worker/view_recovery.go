package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerViewRecovery settles the worker views an earlier engine process left behind.
type WorkerViewRecovery interface {
	RecoverWorkerViews(ctx context.Context) ([]recovery.WorkerRuntimeReport, error)
}

// ViewRecoveryError is the typed reason a worker could not resume after a restart. Its reason
// is the structured fact; its text is diagnostic only.
type ViewRecoveryError struct {
	WorkerID string
	Reason   recovery.RecoveryReason
	Cause    error
}

func (e *ViewRecoveryError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("worker %s view recovery failed (%s): %v", e.WorkerID, e.Reason, e.Cause)
	}
	return fmt.Sprintf("worker %s view recovery failed (%s)", e.WorkerID, e.Reason)
}

func (e *ViewRecoveryError) Unwrap() error { return e.Cause }

// SettleWorkerViewsAtStartup settles every recorded view, then resumes or fails each active
// worker that lost one. Runs before the poller can claim, so no live worker is in scope.
func SettleWorkerViewsAtStartup(ctx context.Context, queue WorkerQueue, views WorkerViewRecovery) error {
	if views == nil {
		return nil
	}
	reports, err := views.RecoverWorkerViews(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, report := range reports {
		task, ok := queue.Get(report.WorkerID)
		if !ok || task == nil || task.Status.IsTerminal() || strings.TrimSpace(task.WorkspaceRoot) == "" {
			continue
		}
		if err := settleRecoveredTask(ctx, queue, task, report); err != nil {
			errs = append(errs, fmt.Errorf("recover worker %s view: %w", report.WorkerID, err))
		}
	}
	return errors.Join(errs...)
}

func settleRecoveredTask(ctx context.Context, queue WorkerQueue, task *api.WorkerTask, report recovery.WorkerRuntimeReport) error {
	if !report.Deletable() {
		return fenceUnresolvedTask(ctx, queue, task.ID)
	}
	if !report.HadRuntime {
		return nil
	}
	if report.Reason != recovery.ReasonRecovered {
		return failRecoveredTask(ctx, queue, task, &ViewRecoveryError{WorkerID: task.ID, Reason: report.Reason, Cause: report.Err})
	}
	_, lease, err := queue.EnsureWorkerBranch(ctx, task.ID)
	if err != nil {
		return failRecoveredTask(ctx, queue, task, &ViewRecoveryError{WorkerID: task.ID, Reason: recovery.ReasonCheckpointUnusable, Cause: err})
	}
	lease.Release()
	return nil
}

// fenceUnresolvedTask stops any claim. Cancellation recovery then retries settlement until the
// job can finish without deleting what custody still protects.
func fenceUnresolvedTask(ctx context.Context, queue WorkerQueue, jobID string) error {
	_, err := queue.RequestCancellation(ctx, jobID)
	return err
}

func failRecoveredTask(ctx context.Context, queue WorkerQueue, task *api.WorkerTask, failure *ViewRecoveryError) error {
	if task.Status == api.WorkerStatusRunning && strings.TrimSpace(task.ClaimToken) != "" {
		_, err := queue.Fail(ctx, task, failure)
		return err
	}
	return queue.Cancel(ctx, task.ID, &api.WorkerResult{Summary: failure.Error(), HintCode: string(failure.Reason), HostAssembled: true})
}
