package worker

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/session/workerresults"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerRunStopSession records worker hold/cancel outcomes on the coordinator transcript.
type WorkerRunStopSession interface {
	Hold(ctx context.Context, parentID string, in workerresults.HoldInput) error
}

// DelegationRunStopper marks active delegations canceled for a workflow run.
type DelegationRunStopper interface {
	CancelActiveByWorkflowRunID(ctx context.Context, runID string) error
}

// RunStopService coordinates worker and delegation shutdown.
type RunStopService struct {
	Queue         WorkerQueue
	Holds         WorkerRunStopSession
	Cancellations WorkerCancellationProjection
	Reports       ChangeReportDeps
	Delegations   DelegationRunStopper
}

// CancelWorkersByRunID cancels and settles every worker in the run.
func (s *RunStopService) CancelWorkersByRunID(ctx context.Context, runID, reason string) error {
	if s == nil || s.Queue == nil {
		return nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	tasks, err := s.Queue.ListByWorkflowRunID(ctx, runID,
		api.WorkerStatusPending, api.WorkerStatusHeld, api.WorkerStatusRunning, api.WorkerStatusWaiting)
	if err != nil {
		return err
	}
	var errs []error
	if err := requestWorkerCancellations(ctx, s.Queue, tasks); err != nil {
		errs = append(errs, err)
	}
	for _, task := range tasks {
		if err := s.cancelTask(ctx, task, reason); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SettleWorkerCancellationsByRunID drains exactly the requests committed by the workflow transition.
func (s *RunStopService) SettleWorkerCancellationsByRunID(ctx context.Context, runID, reason string) error {
	if s == nil || s.Queue == nil {
		return nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	tasks, err := s.Queue.ListCancellationRequestsByRunID(ctx, runID)
	if err != nil {
		return err
	}
	var errs []error
	for _, task := range tasks {
		if err := s.cancelTask(ctx, task, reason); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// HoldPendingWorkersByRunID moves pending jobs to held for a workflow run.
func (s *RunStopService) HoldPendingWorkersByRunID(ctx context.Context, runID string) error {
	if s == nil || s.Queue == nil {
		return nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	tasks, err := s.Queue.ListByWorkflowRunID(ctx, runID, api.WorkerStatusPending, api.WorkerStatusHeld)
	if err != nil {
		return err
	}
	var errs []error
	for _, task := range tasks {
		if err := s.holdTask(ctx, task); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// CancelDelegationsByRunID marks active delegations canceled for a workflow run.
func (s *RunStopService) CancelDelegationsByRunID(ctx context.Context, runID string) error {
	if s == nil || s.Delegations == nil {
		return nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	return s.Delegations.CancelActiveByWorkflowRunID(ctx, runID)
}

func (s *RunStopService) cancelTask(ctx context.Context, task api.WorkerTask, reason string) error {
	jobID := strings.TrimSpace(task.ID)
	if task.Status.IsTerminal() {
		return nil
	}
	if err := s.Queue.StopExecution(ctx, jobID); err != nil {
		return err
	}
	if current, ok := s.Queue.Get(jobID); ok {
		task = *current
	}
	if task.Status.IsTerminal() {
		return nil
	}
	result := hostCancellationResult(ctx, task, reason, s.reports())
	report := *result.ChangeReport
	if err := s.Queue.FinishCanceled(ctx, jobID, &result); err != nil {
		return err
	}
	sessionID := strings.TrimSpace(task.ParentSessionID)
	if s.Cancellations != nil && sessionID != "" {
		return s.Cancellations.Append(ctx, sessionID, workeroutcomes.CancellationInput{
			JobID:          jobID,
			AgentType:      task.AgentType,
			ChildSessionID: task.ChildSessionID,
			Reason:         reason,
			Report:         report,
			Result:         result,
		})
	}
	return nil
}

func (s *RunStopService) holdTask(ctx context.Context, task api.WorkerTask) error {
	if task.Status != api.WorkerStatusPending && task.Status != api.WorkerStatusHeld {
		return nil
	}
	if task.Status == api.WorkerStatusPending {
		if err := s.Queue.Hold(ctx, task.ID); err != nil {
			return err
		}
	}
	sessionID := strings.TrimSpace(task.ParentSessionID)
	if s.Holds == nil || sessionID == "" {
		return nil
	}
	report := BuildChangeReport(ctx, task, s.reports())
	return s.Holds.Hold(ctx, sessionID, workerresults.HoldInput{
		JobID:          task.ID,
		AgentType:      task.AgentType,
		ChildSessionID: task.ChildSessionID,
		Report:         report,
	})
}

func (s *RunStopService) reports() ChangeReportDeps {
	if s == nil {
		return ChangeReportDeps{}
	}
	return s.Reports
}

var _ interface {
	CancelWorkersByRunID(ctx context.Context, runID, reason string) error
	SettleWorkerCancellationsByRunID(ctx context.Context, runID, reason string) error
	HoldPendingWorkersByRunID(ctx context.Context, runID string) error
	CancelDelegationsByRunID(ctx context.Context, runID string) error
} = (*RunStopService)(nil)
