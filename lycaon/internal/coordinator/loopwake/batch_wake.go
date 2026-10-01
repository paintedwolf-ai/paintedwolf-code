package loopwake

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/pkg/api"
)

func (l *LoopEngine) coordinatorBatchState(ctx context.Context, sessionID string) batch.State {
	if l == nil {
		return batch.State{Phase: batch.PhasePreDispatch, Seq: 0}
	}
	_, vars, ok := l.activeRunAndVars(ctx, sessionID)
	if !ok || vars == nil {
		return batch.State{Phase: batch.PhasePreDispatch, Seq: 0}
	}
	return batch.Read(vars)
}

func isStaleBatchSeq(live batch.State, env anchor.Envelope) bool {
	if !env.BatchSeqSet || env.BatchSeq <= 0 {
		return false
	}
	return env.BatchSeq < live.Seq
}

// DisarmTimerBackstop removes the timer and ends sleeps with no trigger left.
func (l *LoopEngine) DisarmTimerBackstop(ctx context.Context, sessionID string) {
	if l == nil {
		return
	}
	st := l.sleepState(sessionID)
	st.mu.Lock()
	cancelSleepTimerLocked(st)
	if len(st.waitTriggers) == 0 {
		st.mu.Unlock()
		return
	}
	var kept []WaitTrigger
	for _, t := range st.waitTriggers {
		if t == WaitTriggerTimer {
			continue
		}
		kept = append(kept, t)
	}
	st.waitTriggers = kept
	var closed WaitLease
	var hadLease bool
	if len(kept) == 0 {
		st.armed = false
		st.until = time.Time{}
		st.reason = ""
		closed, hadLease = closeWaitLeaseLocked(st, sessionID)
	}
	st.mu.Unlock()
	if hadLease {
		l.publishWaitLease(ctx, closed)
	}
}

func (l *LoopEngine) dropStaleBatchWake(ctx context.Context, sessionID string, live batch.State) {
	deps := l.loopDeps()
	if deps.DropPendingKicksBeforeBatchSeq != nil && live.Seq > 0 {
		deps.DropPendingKicksBeforeBatchSeq(sessionID, live.Seq)
	}
}

func (l *LoopEngine) dropClosedBatchPendingKicks(ctx context.Context, sessionID string, live batch.State) {
	deps := l.loopDeps()
	if deps.DropPendingKicksForBatchSeq != nil && live.Seq > 0 {
		deps.DropPendingKicksForBatchSeq(sessionID, live.Seq)
	}
}

func (l *LoopEngine) maybeDisarmTimerOnBatchTerminal(ctx context.Context, sessionID string) {
	live := l.coordinatorBatchState(ctx, sessionID)
	switch live.Phase {
	case batch.PhaseSynthesize, batch.PhaseClosed:
		l.DisarmTimerBackstop(ctx, sessionID)
	}
}

func (l *LoopEngine) hostWakeSkipReason(ctx context.Context, in HostWakeActionableInput) (string, bool) {
	if l == nil {
		return "", false
	}
	// Always-wake facts bypass worker-idle gating.
	// Dormant terminal transitions do not start another model turn.
	if isAlwaysWakeInform(in.Inform) || (isAlwaysWakeNudge(in.Wake) && strings.TrimSpace(in.CompletingJobID) != "") {
		if l.workflowRunFinished(ctx, in.SessionID) && !l.overlayPromoteDue(ctx, in.SessionID, in.Env) && !in.Env.HasWorkerDecision() {
			return "skip:workflow_terminal", true
		}
		if phaseAdvancedWake(in) &&
			!l.overlayPromoteDue(ctx, in.SessionID, in.Env) &&
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
	if in.Wake == anchor.WaitTimerFired && l.workflowObligationsOpen(ctx, in.SessionID) {
		return "", false
	}
	if in.Wake == anchor.WaitTimerFired && live.Phase == batch.PhaseClosed {
		l.disarmTimerOnClosedBatch(ctx, in.SessionID, live)
		return "skip:batch_closed", true
	}
	if l.overlayPromoteDue(ctx, in.SessionID, in.Env) {
		return "", false
	}
	// A worker decision stays actionable during sibling work.
	if in.Env.HasWorkerDecision() {
		return "", false
	}
	actionable := l.hostWakeActionable(ctx, in)
	if in.Wake == anchor.WaitTimerFired {
		if !actionable {
			l.DisarmTimerBackstop(ctx, in.SessionID)
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
	if l.workerCycleIdle(ctx, in.SessionID, in.CompletingJobID) {
		return "", false
	}
	return "skip_turn_rearm", true
}

func phaseAdvancedWake(in HostWakeActionableInput) bool {
	return in.Wake == anchor.PhaseAdvanced || in.Inform == anchor.PhaseAdvanced
}

func (l *LoopEngine) hostWakeActionable(ctx context.Context, in HostWakeActionableInput) bool {
	deps := l.loopDeps()
	return deps.HostWakeActionable == nil || deps.HostWakeActionable(ctx, in)
}

func (l *LoopEngine) disarmTimerOnClosedBatch(ctx context.Context, sessionID string, live batch.State) {
	l.DisarmTimerBackstop(ctx, sessionID)
	l.dropClosedBatchPendingKicks(ctx, sessionID, live)
}

func (l *LoopEngine) workflowRunFinished(ctx context.Context, sessionID string) bool {
	if l == nil {
		return false
	}
	deps := l.loopDeps()
	if deps.WorkflowSource == nil {
		return false
	}
	run, err := deps.WorkflowSource.ActiveRun(ctx, sessionID)
	if err != nil {
		return false
	}
	return run == nil || run.Status != api.WorkflowRunStatusRunning
}
