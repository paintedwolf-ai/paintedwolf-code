package loopwake

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/pkg/api"
)

type HostWakePolicyDeps struct {
	DropPendingKicksBeforeBatchSeq func(sessionID string, liveSeq int)
	DropPendingKicksForBatchSeq    func(sessionID string, batchSeq int)
	HostWakeActionable             func(ctx context.Context, in HostWakeActionableInput) bool
	WorkflowSource                 *WorkflowDomains
}
type HostWakePolicy struct {
	depsMu        sync.RWMutex
	deps          HostWakePolicyDeps
	Facts         *SessionFacts
	Subscriptions *WaitSubscriptions
	Waits         *Waits
	Cycles        *WorkerCycles
}

func (l *HostWakePolicy) setDeps(deps HostWakePolicyDeps) {
	l.depsMu.Lock()
	l.deps = deps
	l.depsMu.Unlock()
}
func (l *HostWakePolicy) loopDeps() HostWakePolicyDeps {
	if l == nil {
		return HostWakePolicyDeps{}
	}
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.deps
}
func (l *HostWakePolicy) coordinatorBatchState(ctx context.Context, sessionID string) batch.State {
	if l == nil {
		return batch.State{Phase: batch.PhasePreDispatch, Seq: 0}
	}
	_, vars, ok := l.Facts.activeRunAndVars(ctx, sessionID)
	if !ok || vars == nil {
		return batch.State{Phase: batch.PhasePreDispatch, Seq: 0}
	}
	return batch.Read(vars)
}
func (l *HostWakePolicy) dropStaleBatchWake(ctx context.Context, sessionID string, live batch.State) {
	deps := l.loopDeps()
	if deps.DropPendingKicksBeforeBatchSeq != nil && live.Seq > 0 {
		deps.DropPendingKicksBeforeBatchSeq(sessionID, live.Seq)
	}
}
func (l *HostWakePolicy) dropClosedBatchPendingKicks(ctx context.Context, sessionID string, live batch.State) {
	deps := l.loopDeps()
	if deps.DropPendingKicksForBatchSeq != nil && live.Seq > 0 {
		deps.DropPendingKicksForBatchSeq(sessionID, live.Seq)
	}
}
func (l *HostWakePolicy) hostWakeSkipReason(ctx context.Context, in HostWakeActionableInput) (string, bool) {
	if l == nil {
		return "", false
	}
	// Always-wake facts bypass worker-idle gating.
	// Dormant terminal transitions do not start another model turn.
	if isAlwaysWakeInform(in.Inform) || (isAlwaysWakeNudge(in.Wake) && strings.TrimSpace(in.CompletingJobID) != "") {
		if l.workflowRunFinished(ctx, in.SessionID) && !l.Subscriptions.overlayPromoteDue(ctx, in.SessionID, in.Env) && !in.Env.HasWorkerDecision() {
			return "skip:workflow_terminal", true
		}
		if phaseAdvancedWake(in) &&
			!l.Subscriptions.overlayPromoteDue(ctx, in.SessionID, in.Env) &&
			!in.Env.HasWorkerDecision() &&
			!l.hostWakeActionable(ctx, in) {
			return "skip:non_actionable", true
		}
		return "", false
	}
	live := l.coordinatorBatchState(ctx, in.SessionID)
	if isStaleBatchSeq(live, in.Env) {
		l.dropStaleBatchWake(ctx, in.SessionID, live)
		return "drop:stale_batch_seq", true
	}
	// Open workflow obligations keep timer wakes actionable.
	if in.Wake == anchor.WaitTimerFired && l.Facts.workflowObligationsOpen(ctx, in.SessionID) {
		return "", false
	}
	if in.Wake == anchor.WaitTimerFired && live.Phase == batch.PhaseClosed {
		l.Waits.disarmTimerOnClosedBatch(ctx, in.SessionID, live)
		return "skip:batch_closed", true
	}
	if l.Subscriptions.overlayPromoteDue(ctx, in.SessionID, in.Env) {
		return "", false
	}
	// A worker decision stays actionable during sibling work.
	if in.Env.HasWorkerDecision() {
		return "", false
	}
	actionable := l.hostWakeActionable(ctx, in)
	if in.Wake == anchor.WaitTimerFired {
		if !actionable {
			l.Waits.DisarmTimerBackstop(ctx, in.SessionID)
			return "skip:non_actionable", true
		}
	}
	if actionable {
		return "", false
	}
	if phaseAdvancedWake(in) {
		return "skip:non_actionable", true
	}
	// Post-turn drain reuses the existing terminal fact.
	if in.PostTurnDrain && in.Wake == anchor.WorkerTaskFinished {
		return "skip:non_actionable", true
	}
	if in.Wake == anchor.WaitTimerFired {
		return "", false
	}
	if strings.TrimSpace(in.CompletingJobID) == "" && in.Wake != anchor.WorkerTaskFinished {
		return "", false
	}
	if l.Cycles.workerCycleIdle(ctx, in.SessionID, in.CompletingJobID) {
		return "", false
	}
	return "skip_turn_rearm", true
}
func (l *HostWakePolicy) hostWakeActionable(ctx context.Context, in HostWakeActionableInput) bool {
	deps := l.loopDeps()
	return deps.HostWakeActionable == nil || deps.HostWakeActionable(ctx, in)
}
func (l *HostWakePolicy) workflowRunFinished(ctx context.Context, sessionID string) bool {
	if l == nil {
		return false
	}
	deps := l.loopDeps()
	if deps.WorkflowSource == nil {
		return false
	}
	run, err := deps.WorkflowSource.Runs.ActiveBySession(ctx, sessionID)
	if err != nil {
		return false
	}
	return run == nil || run.Status != api.WorkflowRunStatusRunning
}
