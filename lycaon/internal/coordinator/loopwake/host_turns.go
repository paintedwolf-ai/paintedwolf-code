package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/promptresult"
	"strings"
	"sync"
)

type HostTurnsDeps struct {
	HostTurnBlocked func(context.Context, string) bool
	RunPrompt       func(ctx context.Context, sessionID string) (*promptresult.Result, error)
}
type HostTurns struct {
	depsMu              sync.RWMutex
	deps                HostTurnsDeps
	promptActive        sync.Map
	asyncTurns          sync.WaitGroup
	asyncTurnsMu        sync.Mutex
	asyncTurnsBySession map[string]map[*asyncTurnWork]struct{}
	Admission           *Admission
	Policy              *HostWakePolicy
	Nudges              *Nudges
	Observations        *PromptObservations
	Facts               *SessionFacts
	Subscriptions       *WaitSubscriptions
	Waits               *Waits
	Cycles              *WorkerCycles
}

func (l *HostTurns) setDeps(deps HostTurnsDeps) { l.depsMu.Lock(); l.deps = deps; l.depsMu.Unlock() }
func (l *HostTurns) loopDeps() HostTurnsDeps {
	if l == nil {
		return HostTurnsDeps{}
	}
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.deps
}
func (l *HostTurns) runPromptSync(ctx context.Context, sessionID string, pending pendingLoopWake) bool {
	if l.hostTurnBlocked(ctx, sessionID) {
		l.Nudges.enqueuePending(sessionID, pending)
		return false
	}
	if _, loaded := l.promptActive.LoadOrStore(sessionID, struct{}{}); loaded {
		loopLogNudge(sessionID, pending.wake, pending.inform, pending.legID, pending.completingJobID, "prompt_active_requeue")
		l.Nudges.deferPromptWake(ctx, sessionID, pending)
		return false
	}
	defer l.releasePromptActiveAndRedrain(ctx, sessionID)
	wake := pending.wake
	if l.Observations.wakeConsumed(ctx, sessionID, pending) {
		return true
	}
	if l.Admission.PromptExecutionActive(sessionID) {
		loopLogNudge(sessionID, wake, pending.inform, pending.legID, pending.completingJobID, "sync_defer_busy")
		l.Nudges.deferPromptWake(ctx, sessionID, pending)
		return false
	}
	// Explicit wait delivery precedes optional workflow gates.
	if l.Subscriptions.routeWaitWake(ctx, sessionID, pending) {
		return true
	}
	allow, busy := l.Admission.evaluate(ctx, sessionID, wake)
	if busy {
		l.Nudges.deferPromptWake(ctx, sessionID, pending)
		return false
	}
	if !allow {
		return true
	}
	if pending.completingJobID == "" && wake != anchor.WorkerBudgetRequested && !pending.env.HasWorkerDecision() && !l.Cycles.workerCycleIdle(ctx, sessionID, "") {
		l.Nudges.deferNudge(ctx, sessionID, pending)
		return true
	}
	if reason, skip := l.Policy.hostWakeSkipReason(ctx, pending.actionableInput(sessionID)); skip {
		loopLogNudge(sessionID, wake, pending.inform, pending.legID, pending.completingJobID, "drain_"+reason)
		l.Waits.parkForActiveHold(ctx, sessionID)
		return true
	}

	runID := l.Facts.activeRunID(ctx, sessionID)
	if !l.Admission.ConsumeBudget(ctx, sessionID, runID, wake) {
		loopLogNudge(sessionID, wake, "", "", "", "budget_exhausted")
		return false
	}
	loopLogRunPrompt(sessionID, wake)
	deps := l.loopDeps()
	if deps.RunPrompt != nil {
		_, _ = deps.RunPrompt(ctx, sessionID)
	}
	return true
}
func (l *HostTurns) releasePromptActiveAndRedrain(ctx context.Context, sessionID string) {
	l.promptActive.Delete(sessionID)
	l.Nudges.schedulePendingDrain(ctx, sessionID, true)
}
func (l *HostTurns) spawnAsyncTurn(ctx context.Context, sessionID string, fn func(ctx context.Context)) {
	timeout := l.Facts.sessionLimits(ctx, sessionID).CoordinatorHostTurnTimeout()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	work := l.registerAsyncTurn(sessionID, cancel)
	l.asyncTurns.Add(1)
	go func() {
		defer l.asyncTurns.Done()
		defer l.finishAsyncTurn(sessionID, work)
		defer observability.GuardPanic("coordinator.loopwake.async_turn")
		fn(ctx)
	}()
}
func (l *HostTurns) registerAsyncTurn(sessionID string, cancel context.CancelFunc) *asyncTurnWork {
	work := &asyncTurnWork{cancel: cancel, done: make(chan struct{})}
	l.asyncTurnsMu.Lock()
	if l.asyncTurnsBySession == nil {
		l.asyncTurnsBySession = make(map[string]map[*asyncTurnWork]struct{})
	}
	if l.asyncTurnsBySession[sessionID] == nil {
		l.asyncTurnsBySession[sessionID] = make(map[*asyncTurnWork]struct{})
	}
	l.asyncTurnsBySession[sessionID][work] = struct{}{}
	l.asyncTurnsMu.Unlock()
	return work
}
func (l *HostTurns) finishAsyncTurn(sessionID string, work *asyncTurnWork) {
	if work == nil {
		return
	}
	l.asyncTurnsMu.Lock()
	delete(l.asyncTurnsBySession[sessionID], work)
	if len(l.asyncTurnsBySession[sessionID]) == 0 {
		delete(l.asyncTurnsBySession, sessionID)
	}
	close(work.done)
	l.asyncTurnsMu.Unlock()
	work.cancel()
}
func (l *HostTurns) cancelAndDrainAsyncTurns(sessionID string) {
	l.asyncTurnsMu.Lock()
	works := make([]*asyncTurnWork, 0, len(l.asyncTurnsBySession[sessionID]))
	for work := range l.asyncTurnsBySession[sessionID] {
		works = append(works, work)
		work.cancel()
	}
	l.asyncTurnsMu.Unlock()
	for _, work := range works {
		<-work.done
	}
}
func (l *HostTurns) cancelAllAsyncTurns() {
	l.asyncTurnsMu.Lock()
	var cancels []context.CancelFunc
	for _, set := range l.asyncTurnsBySession {
		for work := range set {
			cancels = append(cancels, work.cancel)
		}
	}
	l.asyncTurnsMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
func (l *HostTurns) WaitForAsyncTurns(ctx context.Context) {
	if l == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		l.asyncTurns.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-ctx.Done():
	}
	l.cancelAllAsyncTurns()
	<-done
}

func (l *HostTurns) BeginUserTurnSettlement(ctx context.Context, sessionID string) (func(), bool) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return func() {}, false
	}
	if l.Admission.PromptExecutionActive(sessionID) || (!l.hostTurnBlocked(ctx, sessionID) && l.Nudges.HasPendingLoopWakes(sessionID)) {
		return func() {}, false
	}
	if _, loaded := l.promptActive.LoadOrStore(sessionID, struct{}{}); loaded {
		return func() {}, false
	}
	if l.Admission.PromptExecutionActive(sessionID) || (!l.hostTurnBlocked(ctx, sessionID) && l.Nudges.HasPendingLoopWakes(sessionID)) {
		l.releasePromptActiveAndRedrain(ctx, sessionID)
		return func() {}, false
	}
	return func() { l.releasePromptActiveAndRedrain(ctx, sessionID) }, true
}
func (l *HostTurns) Active(sessionID string) bool {
	_, active := l.promptActive.Load(sessionID)
	return active
}
