package loopwake

import (
	"context"
	"fmt"
	"strings"
	"sync"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/pkg/api"
)

type WaitSubscriptionsDeps struct {
	GetSession                func(ctx context.Context, sessionID string) (*api.Session, error)
	HostWakeOverlayPromoteDue func(ctx context.Context, sessionID string) bool
	ProcessReport             func(sessionID, handle string) (report string, published bool)
	ProcessRunning            func(sessionID string, handles []string) bool
	ProcessState              func(sessionID, handle string) (known, running bool)
	ScanCycleOpen             func(ctx context.Context, sessionID string) bool
	WorkerCycleIdle           WorkerCycleIdle
}
type WaitSubscriptions struct {
	Policy *HostWakePolicy

	depsMu     sync.RWMutex
	deps       WaitSubscriptionsDeps
	waitStore  *awaitstore.Store
	Nudges     *Nudges
	Facts      *SessionFacts
	Deliveries *WaitDeliveries
	Waits      *Waits
	Cycles     *WorkerCycles
}

func (l *WaitSubscriptions) setDeps(deps WaitSubscriptionsDeps) {
	l.depsMu.Lock()
	l.deps = deps
	l.depsMu.Unlock()
}
func (l *WaitSubscriptions) loopDeps() WaitSubscriptionsDeps {
	if l == nil {
		return WaitSubscriptionsDeps{}
	}
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.deps
}
func (l *WaitSubscriptions) SetWaitStore(store *awaitstore.Store) {
	if l == nil {
		return
	}
	l.depsMu.Lock()
	l.waitStore = store
	l.depsMu.Unlock()
}
func (l *WaitSubscriptions) durableWaitStore() *awaitstore.Store {
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.waitStore
}
func (l *WaitSubscriptions) settleDurableWaitWake(
	ctx context.Context,
	sessionID string,
	wake, inform anchor.ID,
	legID, completingJobID string,
	env anchor.Envelope,
) bool {
	store := l.durableWaitStore()
	if store == nil {
		return false
	}
	lease, active, err := store.ForSession(ctx, sessionID)
	if err != nil {
		return true
	}
	if !active {
		return false
	}
	// The durable subscription exists before its sleep projection is armed.
	triggers, processHandles := triggersFromConditions(lease.Conditions)
	condition, matched := waitConditionForWake(triggers, waitMatchInput{
		Wake: wake, CompletingJobID: completingJobID, ProcessHandle: legID,
		ProcessHandles:    processHandles,
		WorkerHandles:     workerHandlesFromConditions(lease.Conditions),
		CycleIdle:         l.Cycles.workerCycleIdle(ctx, sessionID, completingJobID),
		OverlayPromoteDue: l.overlayPromoteDue(ctx, sessionID, env),
		NeedsDecision:     env.HasWorkerDecision(),
	})
	condition.Report = processWakeReport(wake, env)
	if condition.Outcome == "" {
		condition.Outcome = "satisfied"
	}
	if strings.TrimSpace(lease.WorkerJobID) != "" {
		// Only a subscribed event settles a worker-owned wait.
		if !matched {
			loopLogNudge(sessionID, wake, inform, legID, completingJobID, "worker_wait_subscription_filtered")
			return true
		}
		won, settleErr := store.SettleLease(ctx, lease.ID, "resolved", condition)
		if settleErr != nil || !won {
			return true
		}
		l.Waits.breakSleep(ctx, sessionID, string(wake), true)
		return true
	}
	var won bool
	if matched {
		won, err = store.SettleLease(ctx, lease.ID, "resolved", condition)
	} else {
		won, err = store.SettleLease(ctx, lease.ID, "interrupted", awaitstore.Condition{})
	}
	if err != nil || !won {
		return true
	}
	if matched {
		l.Deliveries.rememberWaitWinner(sessionID, lease.ID, condition)
		l.Waits.breakSleep(ctx, sessionID, string(wake), false)
		l.Deliveries.runWaitResumeAsync(ctx, sessionID)
		return true
	}
	return false
}
func (l *WaitSubscriptions) resolveSubscribedWorkerWait(ctx context.Context, sessionID, jobID string) {
	store := l.durableWaitStore()
	if store == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	lease, active, err := store.ForSession(ctx, sessionID)
	if err != nil || !active || lease.WorkerJobID != "" {
		return
	}
	triggers, _ := triggersFromConditions(lease.Conditions)
	winner, matched := waitConditionForWake(triggers, waitMatchInput{
		Wake: anchor.WorkerTaskFinished, CompletingJobID: jobID,
		CycleIdle: l.Cycles.workerCycleIdle(ctx, sessionID, jobID),
	})
	if !matched {
		return
	}
	winner.Outcome = "satisfied"
	won, err := store.SettleLease(ctx, lease.ID, "resolved", winner)
	if err != nil || !won {
		return
	}
	l.Deliveries.rememberWaitWinner(sessionID, lease.ID, winner)
	l.Waits.breakSleep(ctx, sessionID, string(anchor.WorkerTaskFinished), false)
	l.Deliveries.runWaitResumeAsync(ctx, sessionID)
}
func (l *WaitSubscriptions) processConditionOutcome(sessionID string, condition awaitstore.Condition) (awaitstore.Condition, bool) {
	deps := l.loopDeps()
	for _, handle := range condition.Handles {
		known, running := false, false
		if deps.ProcessState != nil {
			known, running = deps.ProcessState(sessionID, handle)
		}
		if known && running {
			continue
		}
		condition.Handles = []string{handle}
		if !known {
			condition.Outcome = "unavailable"
			return condition, true
		}
		if deps.ProcessReport != nil {
			report, published := deps.ProcessReport(sessionID, handle)
			if !published {
				continue
			}
			condition.Report = report
		}
		condition.Outcome = "satisfied"
		return condition, true
	}
	return awaitstore.Condition{}, false
}
func (l *WaitSubscriptions) scanCycleOpen(ctx context.Context, sessionID string) bool {
	if l == nil {
		return false
	}
	deps := l.loopDeps()
	if deps.ScanCycleOpen == nil {
		return false
	}
	return deps.ScanCycleOpen(ctx, sessionID)
}
func (l *WaitSubscriptions) processCycleOpen(sessionID string, handles []string) bool {
	if l == nil {
		return false
	}
	deps := l.loopDeps()
	if deps.ProcessRunning == nil {
		return false
	}
	return deps.ProcessRunning(sessionID, handles)
}
func (l *WaitSubscriptions) overlayPromoteDue(ctx context.Context, sessionID string, env anchor.Envelope) bool {
	if env.HasPendingOverlayPromote() {
		return true
	}
	deps := l.loopDeps()
	if deps.HostWakeOverlayPromoteDue == nil {
		return false
	}
	return deps.HostWakeOverlayPromoteDue(ctx, sessionID)
}
func (l *WaitSubscriptions) waitWakeAccepted(
	ctx context.Context,
	sessionID string,
	wake, inform anchor.ID,
	legID string,
	completingJobID string,
	env anchor.Envelope,
) bool {
	if isAlwaysWakeInform(inform) || isAlwaysWakeNudge(wake) {
		return true
	}
	if !l.Waits.IsSleeping(sessionID) {
		return true
	}
	triggers := l.Waits.Triggers(sessionID)
	in := waitMatchInput{
		Wake:              wake,
		CompletingJobID:   completingJobID,
		ProcessHandle:     legID,
		ProcessHandles:    l.Waits.ActiveProcessHandles(sessionID),
		WorkerHandles:     l.Waits.activeWorkerHandles(sessionID),
		CycleIdle:         l.Cycles.workerCycleIdle(ctx, sessionID, completingJobID),
		OverlayPromoteDue: l.overlayPromoteDue(ctx, sessionID, env),
		NeedsDecision:     env.HasWorkerDecision(),
	}
	return waitEventMatches(triggers, in)
}
func (l *WaitSubscriptions) matchesActiveWait(ctx context.Context, sessionID string, pending pendingLoopWake) bool {
	if !l.Waits.IsSleeping(sessionID) {
		return false
	}
	_, matched := waitConditionForWake(l.Waits.Triggers(sessionID), waitMatchInput{
		Wake: pending.wake, CompletingJobID: pending.completingJobID, ProcessHandle: pending.legID,
		ProcessHandles: l.Waits.ActiveProcessHandles(sessionID), WorkerHandles: l.Waits.activeWorkerHandles(sessionID),
		CycleIdle:         l.Cycles.workerCycleIdle(ctx, sessionID, pending.completingJobID),
		OverlayPromoteDue: l.overlayPromoteDue(ctx, sessionID, pending.env), NeedsDecision: pending.env.HasWorkerDecision(),
	})
	return matched
}
func (l *WaitSubscriptions) routeWaitWake(ctx context.Context, sessionID string, pending pendingLoopWake) bool {
	wake, inform, legID, completingJobID, env := pending.wake, pending.inform, pending.legID, pending.completingJobID, pending.env
	if l.resumePendingWait(ctx, sessionID, wake) {
		return true
	}
	triggers := l.Waits.Triggers(sessionID)
	if !l.waitWakeAccepted(ctx, sessionID, wake, inform, legID, completingJobID, env) {
		if shouldDeferForAllWorkersIdle(triggers, wake, completingJobID) && !l.Cycles.workerCycleIdle(ctx, sessionID, completingJobID) {
			loopLogNudge(sessionID, wake, inform, legID, completingJobID, "defer_subscription_all_idle")
			l.Nudges.deferNudge(ctx, sessionID, pending)
		} else {
			loopLogNudge(sessionID, wake, inform, legID, completingJobID, "subscription_filtered")
		}
		return true
	}
	if l.Waits.IsSleeping(sessionID) || alwaysBreaksSleep(wake, completingJobID) {
		if l.settleDurableWaitWake(ctx, sessionID, wake, inform, legID, completingJobID, env) {
			return true
		}
		l.Waits.breakSleep(ctx, sessionID, string(wake), true)
	}
	return false
}
func (l *WaitSubscriptions) resumePendingWait(ctx context.Context, sessionID string, wake anchor.ID) bool {
	if _, ready := l.Deliveries.waitWinner(sessionID); !ready {
		return false
	}
	l.Waits.breakSleep(ctx, sessionID, string(wake), false)
	l.Deliveries.runWaitResumeAsync(ctx, sessionID)
	return true
}
func (l *WaitSubscriptions) CloseCompletedWorkflowWait(ctx context.Context, sessionID string) (bool, error) {
	deps := l.loopDeps()
	if deps.WorkerCycleIdle != nil {
		if deps.GetSession == nil {
			return false, fmt.Errorf("session lookup not configured")
		}
		sess, err := deps.GetSession(ctx, sessionID)
		if err != nil {
			return false, err
		}
		if sess == nil {
			return false, fmt.Errorf("session %q not found", sessionID)
		}
		idle, err := deps.WorkerCycleIdle(ctx, sess, "")
		if err != nil || !idle {
			return false, err
		}
	}
	if l.Facts.sessionHasPendingUserInput(ctx, sessionID) {
		return false, nil
	}
	if store := l.durableWaitStore(); store != nil {
		_, armed, err := store.ForSession(ctx, sessionID)
		if err != nil || armed {
			return false, err
		}
	}
	l.Waits.clearCompletedWorkflowWait(ctx, sessionID)
	return true, nil
}
