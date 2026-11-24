package loopwake

import (
	"context"
	"errors"
	"strings"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/pkg/api"
)

func (l *WaitSubscriptions) SetWatchStore(store *watch.Store) {
	if l == nil {
		return
	}
	l.depsMu.Lock()
	l.watchStore = store
	l.depsMu.Unlock()
}

func (l *WaitSubscriptions) WatchStore() *watch.Store {
	if l == nil {
		return nil
	}
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.watchStore
}

// BreakSleep clears the runtime sleep projection for a session.
func (l *Waits) BreakSleep(ctx context.Context, sessionID, reason string, preserveDeadline bool) {
	l.breakSleep(ctx, sessionID, reason, preserveDeadline)
}

// NotifyWatchLeaseResolved records the settled watch winner and wakes the coordinator turn.
func (l *WaitDeliveries) NotifyWatchLeaseResolved(ctx context.Context, sessionID, leaseID string, winner awaitstore.Condition) {
	if l == nil {
		return
	}
	l.rememberWaitWinner(sessionID, leaseID, winner)
	if l.Subscriptions != nil && l.Subscriptions.Waits != nil {
		l.Subscriptions.Waits.breakSleep(ctx, sessionID, winner.Kind, false)
	}
	l.runWaitResumeAsync(ctx, sessionID)
}

func watchHandlesFromConditions(conditions []awaitstore.Condition) []string {
	var handles []string
	for _, condition := range conditions {
		if condition.Kind == watch.ConditionWatchMatch {
			handles = append(handles, condition.Handles...)
		}
	}
	return normalizeProcessHandles(handles)
}

func mapWatchReject(err error) error {
	if err == nil {
		return nil
	}
	var unknown *watch.UnknownHandleError
	if errors.As(err, &unknown) {
		return &toolrejection.ToolReject{Code: "WATCH_HANDLE_UNKNOWN", Data: map[string]any{"handle": unknown.Handle}}
	}
	var invalid *watch.ValidationError
	if errors.As(err, &invalid) {
		return &toolrejection.ToolReject{
			Code: invalid.Code,
			Data: map[string]any{"field": invalid.Field, "reason": invalid.Message, "watch_invalid_fields": invalid.InvalidFields()},
		}
	}
	return err
}

func evaluateDirectCondition(ctx context.Context, loop *WaitSubscriptions, sessionID string, request waitRequest, triggers []WaitTrigger) *awaitstore.Condition {
	if len(request.ProcessHandles) > 0 {
		if winner, ready := loop.processConditionOutcome(sessionID, awaitstore.Condition{Kind: "process_done", Handles: request.ProcessHandles}); ready {
			return &winner
		}
	}
	batchPhase := loop.Policy.coordinatorBatchState(ctx, sessionID).Phase
	batchClosed := batchPhase == batch.PhaseClosed
	soloDurationWait := !request.ExplicitConditions && batchPhase == batch.PhasePreDispatch
	pendingUserInput := loop.Facts.sessionHasPendingUserInput(ctx, sessionID)
	if !batchClosed && !pendingUserInput && batchPhase != batch.PhasePreDispatch &&
		waitSubscribesNextWorkerDone(triggers) && loop.Cycles.WorkerCycleIsIdle(ctx, sessionID) {
		return &awaitstore.Condition{Kind: "next_worker_done", Outcome: "satisfied", Report: "The dispatched workers have finished."}
	}
	if !batchClosed && !soloDurationWait && !pendingUserInput && waitSubscribesAllWorkersIdle(triggers) && loop.Cycles.WorkerCycleIsIdle(ctx, sessionID) {
		return &awaitstore.Condition{Kind: "all_workers_idle", Outcome: "satisfied", Report: "No workers are pending or running."}
	}
	if !batchClosed && !pendingUserInput && waitSubscribesScanDone(triggers) && !loop.scanCycleOpen(ctx, sessionID) {
		return &awaitstore.Condition{Kind: "scan_done", Outcome: "satisfied", Report: "No security scans are pending or running for this project."}
	}
	if len(request.ProcessHandles) == 0 && !batchClosed && !pendingUserInput && waitSubscribesProcessDone(triggers) && !loop.processCycleOpen(sessionID, request.ProcessHandles) {
		return &awaitstore.Condition{Kind: "process_done", Outcome: "satisfied", Report: "No selected commands are running for this session."}
	}
	return nil
}

func executeWatchWait(ctx context.Context, loop *WaitSubscriptions, deps WaitToolDeps, args map[string]any, tctx tools.ToolContext, request waitRequest) (string, error) {
	watchStore := loop.WatchStore()
	if watchStore == nil {
		return "", waitRequestReject(errors.New("watch store is not configured"))
	}
	sessionID := tctx.Identity.SessionID
	triggers := ensureTimerBackstop(request.Triggers)
	directWinner := evaluateDirectCondition(ctx, loop, sessionID, request, triggers)

	timeoutMS := request.TimeoutMS
	until, resumed := loop.Waits.ResolveWaitUntil(ctx, sessionID, request.Resume && !request.ExplicitMode, time.Duration(timeoutMS)*time.Millisecond)
	if request.ResumeDeadline.After(watchStore.Clock().Now()) {
		until, resumed = request.ResumeDeadline, true
	}
	if loop.Facts.sessionHasPendingUserInput(ctx, sessionID) && args["timeout_ms"] == nil {
		if pendingUntil := loop.Facts.pendingUserInputWaitDeadline(ctx, sessionID); pendingUntil.After(until) {
			until = pendingUntil
		}
	}

	loop.interruptParkedProgram(ctx, sessionID, true)
	owner := watch.Owner{
		ProjectID:   tctx.Identity.ProjectID,
		SessionID:   sessionID,
		WorkerJobID: tctx.Identity.WorkerJobID,
	}
	selResult, err := watchStore.SelectOrPark(ctx, watch.SelectRequest{
		Owner:        owner,
		Handles:      request.WatchHandles,
		DirectWinner: directWinner,
		AllowPark:    directWinner == nil,
		ParkLease: awaitstore.Lease{
			SessionID:     sessionID,
			RootSessionID: rootSessionID(tctx),
			ProjectID:     tctx.Identity.ProjectID,
			ProjectDir:    tctx.ActiveRootPath(),
			ToolCallID:    tctx.Identity.ToolCallID,
			WorkerJobID:   tctx.Identity.WorkerJobID,
			ProfileID:     tctx.Identity.Agent,
			Deadline:      until,
			UntilComplete: request.UntilComplete,
			Conditions:    request.Conditions,
			LoopbackPorts: append([]uint16(nil), tctx.Local.LoopbackConnectPorts...),
			Reason:        request.Reason,
		},
	})
	if err != nil {
		return "", mapWatchReject(err)
	}

	if !selResult.Parked {
		status := selResult.Winner.Kind
		if status == "" {
			status = watch.ConditionWatchMatch
		}
		if selResult.Winner.Kind == "process_done" {
			ObserveProcessWait(sessionID, selResult.Winner, loop.loopDeps().ObserveProcess)
		}
		out, err := surveyjson.Marshal(WaitToolResult{
			Status:        status,
			Conditions:    []awaitstore.Condition{selResult.Winner},
			ActiveHandles: selResult.ActiveHandles,
		})
		return string(out), err
	}

	lease := selResult.Lease
	worker := strings.TrimSpace(tctx.Identity.WorkerJobID) != ""
	if !worker {
		if winner, ok := loop.Deliveries.waitWinner(sessionID); ok && winner.LeaseID != lease.ID {
			loop.Deliveries.waitWinners.CompareAndDelete(strings.TrimSpace(sessionID), winner)
		}
	}
	sleepTriggers := triggers
	if worker {
		sleepTriggers = removeWaitTrigger(sleepTriggers, WaitTriggerTimer)
	}
	loop.Waits.enterSleep(ctx, sessionID, sleepArm{
		until: until, untilComplete: request.UntilComplete, reason: request.Reason,
		triggers: sleepTriggers, processHandles: request.ProcessHandles, workerHandles: request.WorkerHandles,
		watchHandles: request.WatchHandles,
		mover:        SleepMoverHost, caller: waitCaller{tool: "wait", toolCallID: tctx.Identity.ToolCallID},
	})
	loop.Waits.MarkWaitCalled(sessionID)
	if tctx.Effects.Out != nil {
		tctx.Effects.Out.OwnerRef = lease.ID
		tctx.Effects.Out.Completion = &api.ToolCompletion{
			Operation: "wait", State: parkedState, ResourceKind: waitResourceKind,
			ResourceID: lease.ID, Reason: request.Reason,
		}
	}
	if deps.Store != nil {
		monitorCtx := deps.RuntimeContext
		if monitorCtx == nil {
			monitorCtx = context.Background()
		}
		startConditionMonitor(monitorCtx, loop, deps.Store, lease) //nolint:contextcheck // Monitor follows application lifetime.
	}
	result := WaitToolResult{
		Status: "parked", LeaseID: lease.ID, Reason: request.Reason, UntilComplete: request.UntilComplete,
		Resumed: resumed, Conditions: request.Conditions, ActiveHandles: selResult.ActiveHandles,
	}
	if !until.IsZero() {
		result.WakeAt = until.Format(time.RFC3339)
	}
	if !resumed {
		result.TimeoutMS = timeoutMS
	}
	out, err := surveyjson.Marshal(result)
	return string(out), err
}
