package worker

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/pkg/api"
)

var workerPollerLog = observability.LazyComponent("worker_poller")

const (
	workerTerminalRetryMin      = 100 * time.Millisecond
	workerTerminalRetryMax      = 5 * time.Second
	workerTerminalRetryAttempts = 6
	workerOutcomeDeliveryLimit  = 2 * time.Minute
)

// OutcomeProjection projects terminal worker effects before acknowledgement.
type OutcomeProjection interface {
	OnWorkerComplete(ctx context.Context, jobID string, result api.WorkerResult) error
	OnWorkerFailed(ctx context.Context, jobID string, err error) error
}

type OutcomeRecorder interface {
	OutcomeProjection
	OnOutcomeDelivered(ctx context.Context, task api.WorkerTask)
}

// LocalWorkerPoller executes local worker jobs.
type LocalWorkerPoller struct {
	Queue               WorkerQueue
	Executor            WorkerExecutor
	Outcomes            OutcomeRecorder
	ParentWaiter        *ParentWorkerWaiter
	InstanceID          string
	MaintenanceInterval time.Duration
	sem                 chan struct{}
	capacityWake        chan struct{}
	inflight            sync.WaitGroup
	cancelMu            sync.Mutex
	cancels             map[string]*pollerExecution
}

type pollerExecution struct {
	claimToken string
	cancel     context.CancelCauseFunc
	done       chan struct{}
}

// NewLocalWorkerPoller builds a poller with bounded concurrency.
func NewLocalWorkerPoller(queue WorkerQueue, executor WorkerExecutor, cfg WorkersConfig, outcomes OutcomeRecorder) *LocalWorkerPoller {
	n := cfg.Poller.MaxConcurrency
	if n <= 0 {
		n = 4
	}
	interval := cfg.MaintenanceInterval()
	return &LocalWorkerPoller{
		Queue:               queue,
		Executor:            executor,
		Outcomes:            outcomes,
		InstanceID:          ResolveInstanceID(cfg),
		MaintenanceInterval: interval,
		sem:                 make(chan struct{}, n),
		capacityWake:        make(chan struct{}, 1),
		cancels:             make(map[string]*pollerExecution),
	}
}

// Run claims signaled work and repairs missed notifications.
func (p *LocalWorkerPoller) Run(ctx context.Context) error {
	if p.Queue == nil || p.Executor == nil || p.Outcomes == nil {
		return errors.New("local worker poller: queue, executor, and outcome recorder required")
	}
	if err := p.maintain(ctx); err != nil {
		workerPollerLog.Warn("worker queue maintenance failed", "error", err)
	}
	p.claimAvailable(ctx)
	interval := p.MaintenanceInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var runnable <-chan struct{}
	if source, ok := p.Queue.(RunnableWakeSource); ok {
		runnable = source.RunnableWake()
	}

	for {
		select {
		case <-ctx.Done():
			p.inflight.Wait()
			return ctx.Err()
		case <-runnable:
			p.claimAvailable(ctx)
		case <-p.capacityWake:
			p.claimAvailable(ctx)
		case <-ticker.C:
			if err := p.maintain(ctx); err != nil {
				workerPollerLog.Warn("worker queue maintenance failed", "error", err)
			}
			p.claimAvailable(ctx)
		}
	}
}

func (p *LocalWorkerPoller) maintain(ctx context.Context) error {
	// Renew live claims before the expiry sweep after scheduling pauses.
	p.renewActiveClaims(ctx)
	if err := p.deliverPendingOutcomes(ctx); err != nil {
		workerPollerLog.Warn("worker outcome delivery failed", "error", err)
	}
	if _, err := p.Queue.ResumeReadyWaits(ctx); err != nil {
		return fmt.Errorf("resume ready waits: %w", err)
	}
	recovered, err := p.Queue.RecoverExpiredClaims(ctx)
	p.fanOutRecovered(ctx, recovered)
	return err
}

func (p *LocalWorkerPoller) claimAvailable(ctx context.Context) {
	for {
		// Acquire capacity before starting the claim lease.
		select {
		case p.sem <- struct{}{}:
		default:
			return
		}
		task, err := p.Queue.ClaimNext(ctx, ClaimRequest{
			ClaimedBy:       p.InstanceID,
			ExecutionTarget: api.ExecutionTargetLocal,
		})
		if err != nil {
			<-p.sem
			if !errors.Is(err, ErrNoPendingJobs) && !errors.Is(err, ErrMaxWorkers) && !errors.Is(err, lifecycle.ErrStopping) && ctx.Err() == nil {
				workerPollerLog.Warn("worker claim deferred", "error", err)
			}
			return
		}
		execCtx, execution := p.registerExecution(ctx, task)
		p.inflight.Add(1)
		go func(t *api.WorkerTask, runCtx context.Context, registered *pollerExecution) {
			defer p.inflight.Done()
			defer func() {
				<-p.sem
				p.notifyCapacity()
			}()
			p.execute(runCtx, t)
			p.unregisterExecution(t.ID, registered)
			close(registered.done)
		}(task, execCtx, execution)
	}
}

func (p *LocalWorkerPoller) notifyCapacity() {
	select {
	case p.capacityWake <- struct{}{}:
	default:
	}
}

// Abort joins active execution and releases the task's retained runtime.
func (p *LocalWorkerPoller) Abort(ctx context.Context, taskID string) error {
	p.cancelMu.Lock()
	execution := p.cancels[strings.TrimSpace(taskID)]
	p.cancelMu.Unlock()
	if execution != nil {
		execution.cancel(context.Canceled)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-execution.done:
		}
	}
	if task, found := p.Queue.Get(taskID); found {
		if p.Executor == nil {
			return fmt.Errorf("worker executor not configured")
		}
		return p.Executor.AbortWorkerRuntime(ctx, *task)
	}
	return nil
}

func (p *LocalWorkerPoller) registerExecution(parent context.Context, task *api.WorkerTask) (context.Context, *pollerExecution) {
	ctx, cancel := context.WithCancelCause(parent)
	execution := &pollerExecution{claimToken: task.ClaimToken, cancel: cancel, done: make(chan struct{})}
	p.cancelMu.Lock()
	if previous := p.cancels[task.ID]; previous != nil {
		previous.cancel(context.Canceled)
	}
	p.cancels[task.ID] = execution
	p.cancelMu.Unlock()
	return ctx, execution
}

func (p *LocalWorkerPoller) renewActiveClaims(ctx context.Context) {
	type activeClaim struct {
		id        string
		token     string
		execution *pollerExecution
	}
	p.cancelMu.Lock()
	claims := make([]activeClaim, 0, len(p.cancels))
	for id, execution := range p.cancels {
		claims = append(claims, activeClaim{id: id, token: execution.claimToken, execution: execution})
	}
	p.cancelMu.Unlock()
	for _, claim := range claims {
		renewed, err := p.Queue.RenewClaim(ctx, claim.id, claim.token)
		if err != nil || !renewed {
			claim.execution.cancel(ErrClaimLost)
		}
	}
}

func (p *LocalWorkerPoller) unregisterExecution(taskID string, execution *pollerExecution) {
	p.cancelMu.Lock()
	if p.cancels[taskID] == execution {
		delete(p.cancels, taskID)
	}
	p.cancelMu.Unlock()
	execution.cancel(nil)
}

// fanOutRecovered publishes terminal effects for expired claims.
func (p *LocalWorkerPoller) fanOutRecovered(ctx context.Context, recovered []api.WorkerTask) {
	for i := range recovered {
		p.publishCommittedOutcome(ctx, recovered[i].ID)
	}
}

func (p *LocalWorkerPoller) execute(parentCtx context.Context, task *api.WorkerTask) {
	defer p.recoverExecute(parentCtx, task)
	// A missing token cannot sustain the execution lease.
	if task.ClaimToken == "" {
		workerPollerLog.Warn("worker claimed without a lease token", "job_id", task.ID)
		return
	}
	if errors.Is(context.Cause(parentCtx), context.Canceled) || errors.Is(context.Cause(parentCtx), lifecycle.ErrStopping) {
		settleCtx, cancel := terminalOutcomeContext(parentCtx)
		defer cancel()
		p.settleCancellation(settleCtx, task, nil)
		return
	}
	renewed, err := p.Queue.RenewClaim(parentCtx, task.ID, task.ClaimToken)
	if err != nil || !renewed {
		if cur, ok := p.Queue.Get(task.ID); ok && cur.Status == api.WorkerStatusCanceled {
			return
		}
		if err == nil {
			err = ErrClaimLost
		}
		settleCtx, cancel := terminalOutcomeContext(parentCtx)
		defer cancel()
		if won, _ := p.commitTerminal(settleCtx, task, "failure", func() (bool, error) {
			return p.Queue.Fail(settleCtx, task, err)
		}); !won {
			return
		}
		p.publishCommittedOutcome(settleCtx, task.ID)
		return
	}
	execCtx, stopHeartbeat := p.workerClaimContext(parentCtx, task)
	defer stopHeartbeat()

	projectDir := task.WorkspacePath
	run := RunContextForTask(*task, projectDir)

	workerPollerLog.Info("worker execute start",
		"job_id", task.ID,
		"parent_session_id", task.ParentSessionID,
		"agent_type", task.AgentType,
		"project_dir", projectDir,
	)
	result, err := p.Executor.Execute(execCtx, *task, run)
	if cause := context.Cause(execCtx); errors.Is(cause, ErrClaimLost) || errors.Is(cause, context.Canceled) || errors.Is(cause, lifecycle.ErrStopping) {
		err = cause
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, lifecycle.ErrStopping) || (err == nil && result.Status == "canceled") {
		settleCtx, cancel := terminalOutcomeContext(parentCtx)
		defer cancel()
		var report *api.WorkerResult
		if err == nil {
			report = &result
		}
		p.settleCancellation(settleCtx, task, report)
		return
	}
	if err != nil {
		workerPollerLog.Warn("worker execute failed", "job_id", task.ID, "agent_type", task.AgentType, "err", err)
		settleCtx, cancel := terminalOutcomeContext(parentCtx)
		defer cancel()
		if workerAttemptRetryable(task, err) {
			if won, _ := p.commitTerminal(settleCtx, task, "attempt retry", func() (bool, error) { return p.Queue.Retry(settleCtx, task, err) }); won {
				workerPollerLog.Info("worker attempt returned to queue", "job_id", task.ID, "agent_type", task.AgentType, "attempt", task.Attempt)
			}
			return
		}
		if won, _ := p.commitTerminal(settleCtx, task, "failure", func() (bool, error) { return p.Queue.Fail(settleCtx, task, err) }); won {
			p.publishCommittedOutcome(settleCtx, task.ID)
		}
		return
	}
	if strings.TrimSpace(result.Status) == "waiting" {
		if won, parkErr := p.Queue.Park(parentCtx, task); parkErr != nil || !won {
			if errors.Is(parkErr, lifecycle.ErrStopping) {
				settleCtx, cancel := terminalOutcomeContext(parentCtx)
				defer cancel()
				p.settleCancellation(settleCtx, task, nil)
			} else {
				workerPollerLog.Warn("worker wait park failed", "job_id", task.ID, "error", parkErr)
			}
		}
		return
	}
	settleCtx, cancel := terminalOutcomeContext(parentCtx)
	defer cancel()
	won, commitErr := p.commitTerminal(settleCtx, task, "completion", func() (bool, error) {
		return p.Queue.Complete(settleCtx, task, result)
	})
	if !won {
		if commitErr == nil || errors.Is(commitErr, lifecycle.ErrStopping) {
			return
		}
		workerPollerLog.Warn("worker completion commit failed; transitioning terminal outcome",
			"job_id", task.ID, "agent_type", task.AgentType, "error", commitErr)
		failCtx, cancelFail := terminalOutcomeContext(parentCtx)
		defer cancelFail()
		if workerAttemptRetryable(task, commitErr) {
			if retryWon, _ := p.commitTerminal(failCtx, task, "completion retry", func() (bool, error) {
				return p.Queue.Retry(failCtx, task, commitErr)
			}); retryWon {
				workerPollerLog.Info("worker attempt returned to queue after completion failure",
					"job_id", task.ID, "agent_type", task.AgentType, "attempt", task.Attempt)
			}
			return
		}
		if failWon, _ := p.commitTerminal(failCtx, task, "completion failure", func() (bool, error) {
			return p.Queue.Fail(failCtx, task, commitErr)
		}); failWon {
			p.publishCommittedOutcome(failCtx, task.ID)
		}
		return
	}
	workerPollerLog.Info("worker execute complete",
		"job_id", task.ID,
		"agent_type", task.AgentType,
		"status", result.Status,
	)
	p.publishCommittedOutcome(settleCtx, task.ID)
}

// Cancellation releases retained runtime after execution exits and before workspace cleanup.
func (p *LocalWorkerPoller) settleCancellation(ctx context.Context, task *api.WorkerTask, result *api.WorkerResult) {
	// Only the current cancellation owner releases runtime and records the result.
	if won, _ := p.commitTerminal(ctx, task, "cancellation intent", func() (bool, error) {
		return p.Queue.RequestClaimCancellation(ctx, task)
	}); !won {
		return
	}
	current, ok := p.Queue.Get(task.ID)
	if !ok {
		return
	}
	if err := p.Executor.AbortWorkerRuntime(ctx, *current); err != nil {
		workerPollerLog.Warn("worker cancellation runtime release failed", "job_id", task.ID, "error", err)
		return
	}
	if won, _ := p.commitTerminal(ctx, task, "cancellation", func() (bool, error) {
		err := p.Queue.FinishCanceled(ctx, task.ID, result)
		return err == nil, err
	}); won {
		p.publishCommittedOutcome(ctx, task.ID)
	}
}

func (p *LocalWorkerPoller) commitTerminal(
	ctx context.Context,
	task *api.WorkerTask,
	action string,
	commit func() (bool, error),
) (bool, error) {
	delay := workerTerminalRetryMin
	var lastErr error
	for attempt := 1; attempt <= workerTerminalRetryAttempts; attempt++ {
		won, err := commit()
		if errors.Is(err, lifecycle.ErrStopping) {
			p.settleCancellation(ctx, task, nil)
			return false, err
		}
		if err == nil {
			return won, nil
		}
		lastErr = err
		if attempt == workerTerminalRetryAttempts {
			workerPollerLog.Warn("worker terminal persistence retries exhausted",
				"job_id", task.ID, "agent_type", task.AgentType, "error", err,
				"action", action, "attempts", attempt)
			return false, err
		}
		workerPollerLog.Warn("worker terminal persistence failed; retrying",
			"job_id", task.ID, "agent_type", task.AgentType, "error", err,
			"action", action, "attempt", attempt, "retry_in", delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false, ctx.Err()
		case <-timer.C:
		}
		if delay < workerTerminalRetryMax {
			delay *= 2
			if delay > workerTerminalRetryMax {
				delay = workerTerminalRetryMax
			}
		}
	}
	return false, lastErr
}

func (p *LocalWorkerPoller) workerClaimContext(parent context.Context, task *api.WorkerTask) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(workerClaimLease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewed, err := p.Queue.RenewClaim(ctx, task.ID, task.ClaimToken)
				if err != nil || !renewed {
					cancel(ErrClaimLost)
					return
				}
			}
		}
	}()
	return ctx, func() {
		cancel(nil)
		<-done
	}
}

// recoverExecute confines an executor panic to its worker job.
func (p *LocalWorkerPoller) recoverExecute(ctx context.Context, task *api.WorkerTask) {
	r := recover()
	if r == nil {
		return
	}
	err := fmt.Errorf("worker execute panicked: %v", r)
	workerPollerLog.Warn("worker execute panicked",
		"job_id", task.ID,
		"agent_type", task.AgentType,
		"panic", fmt.Sprintf("%v", r),
		"stack", string(debug.Stack()),
	)
	settleCtx, cancel := terminalOutcomeContext(ctx)
	defer cancel()
	if cur, ok := p.Queue.Get(task.ID); !ok || cur.Status == api.WorkerStatusRunning {
		if won, _ := p.commitTerminal(settleCtx, task, "failure", func() (bool, error) {
			return p.Queue.Fail(settleCtx, task, err)
		}); !won {
			return
		}
	} else {
		return
	}
	p.publishCommittedOutcome(settleCtx, task.ID)
}

func terminalOutcomeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), workerOutcomeDeliveryLimit)
}

func (p *LocalWorkerPoller) publishCommittedOutcome(ctx context.Context, jobID string) {
	task, ok := p.Queue.Get(jobID)
	if !ok || task == nil {
		workerPollerLog.Warn("committed worker outcome missing", "job_id", jobID)
		return
	}
	if err := p.deliverOutcome(ctx, *task); err != nil {
		workerPollerLog.Warn("worker terminal outcome failed", "job_id", jobID, "error", err)
	}
}

// deliverPendingOutcomes retries unacknowledged side effects independently.
func (p *LocalWorkerPoller) deliverPendingOutcomes(ctx context.Context) error {
	if p.Queue == nil {
		return nil
	}
	tasks, err := p.Queue.ListPendingOutcomes(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for i := range tasks {
		if err := p.deliverOutcome(ctx, tasks[i]); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// deliverOutcome projects and acknowledges a committed outcome.
func (p *LocalWorkerPoller) deliverOutcome(ctx context.Context, task api.WorkerTask) error {
	if p.Outcomes == nil {
		return errors.New("outcome recorder required")
	}
	switch task.Status {
	case api.WorkerStatusComplete, api.WorkerStatusHeld:
		result := api.WorkerResult{Status: "complete"}
		if task.Result != nil {
			result = *task.Result
		}
		if err := p.Outcomes.OnWorkerComplete(ctx, task.ID, result); err != nil {
			return fmt.Errorf("deliver completion for %s: %w", task.ID, err)
		}
	case api.WorkerStatusFailed:
		if err := p.Outcomes.OnWorkerFailed(ctx, task.ID, errors.New(strings.TrimSpace(task.Error))); err != nil {
			return fmt.Errorf("deliver failure for %s: %w", task.ID, err)
		}
	case api.WorkerStatusCanceled:
		result := api.WorkerResult{Status: "canceled", Response: "canceled"}
		if task.Result != nil {
			result = *task.Result
			result.Status = "canceled"
		}
		if err := p.Outcomes.OnWorkerComplete(ctx, task.ID, result); err != nil {
			return fmt.Errorf("deliver cancellation for %s: %w", task.ID, err)
		}
	default:
		return nil
	}
	if err := p.Queue.MarkOutcomeDelivered(ctx, task.ID); err != nil {
		return fmt.Errorf("acknowledge outcome for %s: %w", task.ID, err)
	}
	p.Outcomes.OnOutcomeDelivered(ctx, task)
	if p.ParentWaiter != nil {
		p.ParentWaiter.NotifyParent(task.ParentSessionID)
	}
	return nil
}
