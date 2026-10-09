package worker

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	WorkerCancelNotFoundCode        = "WORKER_CANCEL_NOT_FOUND"
	WorkerCancelTerminalCode        = "WORKER_CANCEL_TERMINAL"
	WorkerCancelSessionMismatchCode = "WORKER_CANCEL_SESSION_MISMATCH"
)

// CancelMode selects whether a worker receives a closeout report opportunity.
type CancelMode int

const (
	// CancelGraceful stops the tool loop and runs worker cancellation closeout.
	CancelGraceful CancelMode = iota
	// CancelImmediate aborts in-flight execution with a host-only envelope (stop button / shutdown).
	CancelImmediate
)

// CancelService cancels coordinator-spawned workers and returns a change report.
type CancelRegistration interface {
	Register(string, string, string) error
}
type CancelService struct {
	Graceful      CancelRegistration
	Cancellations WorkerCancellationProjection
	Queue         WorkerQueue
	Events        TerminalNotice
	Reports       ChangeReportDeps
	Reject        *guidance.StaticRejectFormatter
}

// CancelJob cancels by job id with a graceful closeout (HTTP worker pane).
func (s *CancelService) CancelJob(ctx context.Context, jobID, reason string) (api.WorkerCancelResult, error) {
	jobID = strings.TrimSpace(jobID)
	if s == nil || s.Queue == nil {
		return api.WorkerCancelResult{}, fmt.Errorf("worker cancel not configured")
	}
	task, ok := s.Queue.Get(jobID)
	if !ok {
		return api.WorkerCancelResult{}, s.reject(WorkerCancelNotFoundCode, map[string]any{"job_id": jobID})
	}
	return s.cancelTask(ctx, task, strings.TrimSpace(task.ParentSessionID), reason, CancelGraceful)
}

// CancelForSession cancels jobID when it belongs to sessionID (coordinator worker_cancel tool).
func (s *CancelService) CancelForSession(ctx context.Context, sessionID, jobID, reason string) (api.WorkerCancelResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	jobID = strings.TrimSpace(jobID)
	if s == nil || s.Queue == nil {
		return api.WorkerCancelResult{}, fmt.Errorf("worker cancel not configured")
	}
	task, ok := s.Queue.Get(jobID)
	if !ok {
		return api.WorkerCancelResult{}, s.reject(WorkerCancelNotFoundCode, map[string]any{"job_id": jobID})
	}
	if strings.TrimSpace(task.ParentSessionID) != sessionID {
		return api.WorkerCancelResult{}, s.reject(WorkerCancelSessionMismatchCode, map[string]any{"job_id": jobID})
	}
	return s.cancelTask(ctx, task, sessionID, reason, CancelGraceful)
}

// AbortWorkersForRoot immediately aborts in-flight jobs whose sandbox includes rootID.
func (s *CancelService) AbortWorkersForRoot(ctx context.Context, projectID, rootID string, projectRoots []projectroot.RootRef, reason string) error {
	if s == nil || s.Queue == nil {
		return nil
	}
	projectID = strings.TrimSpace(projectID)
	rootID = strings.TrimSpace(rootID)
	if projectID == "" || rootID == "" {
		return nil
	}
	detached, ok := projectroot.RootRefByID(projectRoots, rootID)
	if !ok {
		return nil
	}
	tasks, err := s.Queue.List(ctx, projectID,
		api.WorkerStatusPending, api.WorkerStatusRunning, api.WorkerStatusWaiting, api.WorkerStatusHeld)
	if err != nil {
		return err
	}
	var selected []api.WorkerTask
	for _, task := range tasks {
		if projectroot.WorkerTaskBindsRoot(task, detached, projectRoots) {
			selected = append(selected, task)
		}
	}
	return s.abortTasks(ctx, selected, reason)
}

// AbortAllWorkers immediately aborts every active job, including parked waits (session stop / shutdown).
func (s *CancelService) AbortAllWorkers(ctx context.Context, sessionID, projectID, reason string) error {
	if s == nil || s.Queue == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	projectID = strings.TrimSpace(projectID)
	if sessionID == "" {
		return nil
	}
	tasks, err := s.Queue.ListBySession(
		ctx,
		projectID,
		sessionID,
		api.WorkerStatusPending,
		api.WorkerStatusRunning,
		api.WorkerStatusWaiting,
		api.WorkerStatusHeld,
	)
	if err != nil {
		return err
	}
	return s.abortTasks(ctx, tasks, reason)
}

func (s *CancelService) abortTasks(ctx context.Context, tasks []api.WorkerTask, reason string) error {
	cancelErr := requestWorkerCancellations(ctx, s.Queue, tasks)
	for _, task := range tasks {
		if _, err := s.cancelTask(ctx, &task, strings.TrimSpace(task.ParentSessionID), reason, CancelImmediate); err != nil {
			var reject *toolrejection.ToolReject
			if errors.As(err, &reject) && reject.Code == WorkerCancelTerminalCode {
				continue
			}
			cancelErr = errors.Join(cancelErr, fmt.Errorf("cancel worker %s: %w", task.ID, err))
		}
	}
	return cancelErr
}

// requestWorkerCancellations fences the whole batch before waiting for any one
// worker to exit.
func requestWorkerCancellations(ctx context.Context, queue WorkerQueue, tasks []api.WorkerTask) error {
	var errs []error
	for _, task := range tasks {
		if _, err := queue.RequestCancellation(ctx, task.ID); err != nil {
			errs = append(errs, fmt.Errorf("fence worker %s cancellation: %w", task.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *CancelService) cancelTask(ctx context.Context, task *api.WorkerTask, sessionID, reason string, mode CancelMode) (api.WorkerCancelResult, error) {
	if task == nil {
		return api.WorkerCancelResult{}, fmt.Errorf("task required")
	}
	jobID := strings.TrimSpace(task.ID)
	if task.Status.IsTerminal() {
		return api.WorkerCancelResult{}, s.reject(WorkerCancelTerminalCode, map[string]any{
			"job_id": jobID,
			"status": string(task.Status),
		})
	}

	if task.Status == api.WorkerStatusRunning && mode == CancelGraceful && strings.TrimSpace(task.ChildSessionID) != "" && s.Graceful != nil {
		return s.cancelRunningGraceful(ctx, task, sessionID, reason)
	}
	return s.cancelImmediate(ctx, task, sessionID, reason)
}

func (s *CancelService) cancelRunningGraceful(ctx context.Context, task *api.WorkerTask, sessionID, reason string) (api.WorkerCancelResult, error) {
	childSessionID := strings.TrimSpace(task.ChildSessionID)
	if err := s.Graceful.Register(childSessionID, task.ID, reason); err != nil {
		return s.cancelImmediate(ctx, task, sessionID, reason)
	}
	// The poller finalizes cancellation after the worker closeout.
	return api.WorkerCancelResult{
		JobID:     task.ID,
		Status:    task.Status,
		AgentType: task.AgentType,
		Report:    api.WorkerChangeReport{},
		Result:    api.WorkerResult{},
	}, nil
}

func (s *CancelService) cancelImmediate(ctx context.Context, task *api.WorkerTask, sessionID, reason string) (api.WorkerCancelResult, error) {
	jobID := strings.TrimSpace(task.ID)
	if err := s.Queue.StopExecution(ctx, jobID); err != nil {
		return api.WorkerCancelResult{}, err
	}
	if current, ok := s.Queue.Get(jobID); ok {
		task = current
	}
	// A task a concurrent stop already canceled still gets this stop's transcript record.
	if task.Status.IsTerminal() && task.Status != api.WorkerStatusCanceled {
		return api.WorkerCancelResult{}, s.reject(WorkerCancelTerminalCode, map[string]any{"job_id": jobID, "status": string(task.Status)})
	}
	result := hostCancellationResult(ctx, *task, reason, s.reports())
	report := *result.ChangeReport
	if err := s.Queue.FinishCanceled(ctx, jobID, &result); err != nil {
		return api.WorkerCancelResult{}, err
	}
	if s.Cancellations != nil && sessionID != "" {
		if err := s.Cancellations.Append(ctx, sessionID, workeroutcomes.CancellationInput{
			JobID:          jobID,
			AgentType:      task.AgentType,
			ChildSessionID: task.ChildSessionID,
			Reason:         reason,
			Report:         report,
			Result:         result,
		}); err != nil {
			return api.WorkerCancelResult{}, err
		}
		s.Events.Terminal(ctx, sessionID, jobID)
	}
	got, ok := s.Queue.Get(jobID)
	if !ok {
		return api.WorkerCancelResult{}, fmt.Errorf("job not found after cancel: %s", jobID)
	}
	return api.WorkerCancelResult{
		JobID:     got.ID,
		Status:    got.Status,
		AgentType: got.AgentType,
		Report:    report,
		Result:    result,
	}, nil
}

func (s *CancelService) reports() ChangeReportDeps {
	if s == nil {
		return ChangeReportDeps{}
	}
	return s.Reports
}

func (s *CancelService) reject(code string, data map[string]any) error {
	var formatter *guidance.StaticRejectFormatter
	if s != nil {
		formatter = s.Reject
	}
	return toolrejection.FormatDecisionReject(code, data, formatter)
}

func cancellationSummary(task api.WorkerTask, reason string, report api.WorkerChangeReport) string {
	agent := strings.TrimSpace(task.AgentType)
	if agent == "" {
		agent = "worker"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s canceled", agent)
	if len(report.ChangedPaths) > 0 {
		fmt.Fprintf(&b, " — %d path(s) changed since leg start", len(report.ChangedPaths))
	} else if report.WorkspaceDirty {
		b.WriteString(" — workspace dirty (paths unknown)")
	} else {
		b.WriteString(" — no verified file changes")
	}
	if r := strings.TrimSpace(reason); r != "" {
		fmt.Fprintf(&b, " (%s)", r)
	}
	return b.String()
}
