package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type NudgesDeps struct {
	BoardWillForceInject func(ctx context.Context, sess *api.Session, run api.CoordinatorRunContext) bool
	CoordinatorFrame     inject.CoordinatorTurnFrameSource
	GetSession           func(ctx context.Context, sessionID string) (*api.Session, error)
	HasQueuedKick        func(sessionID, kickID string) bool
	OnLoopQuiescent      func(ctx context.Context, sessionID string)
	QueueInform          func(ctx context.Context, sessionID string, inform anchor.ID, env anchor.Envelope)
}
type Nudges struct {
	depsMu              sync.RWMutex
	deps                NudgesDeps
	pendingDrain        sync.Map
	pendingQueues       sync.Map
	pendingWorkerQueues sync.Map
	kickDedup           sync.Map
	nudgeSeq            atomic.Uint64
	Admission           *Admission
	Turns               *HostTurns
	Policy              *HostWakePolicy
	Observations        *PromptObservations
	Facts               *SessionFacts
	Deliveries          *WaitDeliveries
	Subscriptions       *WaitSubscriptions
	Waits               *Waits
	Cycles              *WorkerCycles
}

func (l *Nudges) setDeps(deps NudgesDeps) { l.depsMu.Lock(); l.deps = deps; l.depsMu.Unlock() }
func (l *Nudges) loopDeps() NudgesDeps {
	if l == nil {
		return NudgesDeps{}
	}
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.deps
}
func (l *Nudges) Nudge(ctx context.Context, sessionID string, wake, inform anchor.ID, legID string, env anchor.Envelope) {
	l.nudgeNow(ctx, sessionID, wake, inform, legID, "", env)
}
func (l *Nudges) NudgeAfterWorkerJobTerminal(
	ctx context.Context,
	sessionID, completingJobID string,
	wake, inform anchor.ID,
	legID string,
	env anchor.Envelope,
) {
	l.nudgeNow(ctx, sessionID, wake, inform, legID, completingJobID, env)
}
func (l *Nudges) nudgeNow(
	ctx context.Context,
	sessionID string,
	wake, inform anchor.ID,
	legID, completingJobID string,
	env anchor.Envelope,
) {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	seq := l.nudgeSeq.Add(1)
	pending := pendingLoopWake{wake: wake, inform: inform, legID: legID, completingJobID: completingJobID, env: env, seq: seq}
	if wake == anchor.PhaseAdvanced {
		if run, _, ok := l.Facts.activeRunAndVars(ctx, sessionID); ok && run != nil {
			pending.runID, pending.revision = run.ID, run.Revision
		}
	}
	l.deliverNudge(ctx, sessionID, pending)
}
func (l *Nudges) deliverNudge(ctx context.Context, sessionID string, pending pendingLoopWake) {
	wake, inform, legID, completingJobID, env := pending.wake, pending.inform, pending.legID, pending.completingJobID, pending.env
	deps := l.loopDeps()
	if l.Subscriptions.resumePendingWait(ctx, sessionID, pending.wake) {
		return
	}
	// A subscribed result belongs to its lease, even after its originating batch closes.
	if l.Subscriptions.matchesActiveWait(ctx, sessionID, pending) && l.Subscriptions.routeWaitWake(ctx, sessionID, pending) {
		return
	}
	liveBatch := l.Policy.coordinatorBatchState(ctx, sessionID)
	if isStaleBatchSeq(liveBatch, env) {
		l.Policy.dropStaleBatchWake(ctx, sessionID, liveBatch)
		loopLogNudge(sessionID, wake, inform, legID, completingJobID, "drop:stale_batch_seq")
		return
	}
	if l.Observations.wakeConsumed(ctx, sessionID, pending) || l.Subscriptions.routeWaitWake(ctx, sessionID, pending) {
		return
	}
	blocked := l.Turns.hostTurnBlocked(ctx, sessionID)
	// A budget request is actionable while its job is running.
	perJobWake := strings.TrimSpace(completingJobID) != "" || wake == anchor.WorkerBudgetRequested
	if !l.Cycles.workerCycleIdle(ctx, sessionID, completingJobID) && !perJobWake {
		loopLogNudge(sessionID, wake, inform, legID, completingJobID, "defer_worker_cycle")
		l.deferNudge(ctx, sessionID, pending)
		return
	}
	runID := l.Facts.activeRunID(ctx, sessionID)
	allow, busy := l.Admission.evaluate(ctx, sessionID, wake)
	if !l.Cycles.workerCycleIdle(ctx, sessionID, "") && !perJobWake {
		loopLogNudge(sessionID, wake, inform, legID, completingJobID, "defer_worker_cycle_idle")
		l.deferNudge(ctx, sessionID, pending)
		return
	}
	skipIn := HostWakeActionableInput{
		SessionID:       sessionID,
		Wake:            wake,
		Inform:          inform,
		CompletingJobID: completingJobID,
		Env:             env,
	}
	// Prompt execution defers actionability until the wake drains.
	if !busy && !blocked {
		if reason, skip := l.Policy.hostWakeSkipReason(ctx, skipIn); skip {
			loopLogNudge(sessionID, wake, inform, legID, completingJobID, reason)
			if reason == "skip_turn_rearm" {
				l.Waits.rearmSleepAfterSkip(ctx, sessionID)
			} else {
				// Rejected wakes preserve the active hold.
				l.Waits.parkForActiveHold(ctx, sessionID)
			}
			return
		}
	}
	dedupLegID := kickDedupLegID(inform, legID, completingJobID)
	shouldKick := !pending.informHandled && inform != "" && !l.recentKick(sessionID, runID, dedupLegID, wake)
	if shouldKick && inform == anchor.LegFinished && anchor.OmitInformWhenBoardReinjected(inform) &&
		deps.BoardWillForceInject != nil && deps.CoordinatorFrame != nil {
		if sess, err := deps.GetSession(ctx, sessionID); err == nil && sess != nil {
			frame, err := deps.CoordinatorFrame.BuildCoordinatorTurnFrame(ctx, sessionID, sess)
			if err == nil && deps.BoardWillForceInject(ctx, sess, frame.RunContext) {
				shouldKick = false
			}
		}
	}
	if shouldKick && deps.QueueInform != nil {
		emitEnv := env
		// A per-subject fact queues beside the same kick for other subjects.
		emitEnv.Subject = dedupLegID
		if liveBatch.Seq > 0 {
			emitEnv.BatchSeq = liveBatch.Seq
			emitEnv.BatchSeqSet = true
		}
		deps.QueueInform(ctx, sessionID, inform, emitEnv)
		l.markKick(sessionID, runID, dedupLegID, wake)
	}
	pending.informHandled = true
	if blocked {
		l.deferPromptWake(ctx, sessionID, pending)
		return
	}
	if !allow {
		loopLogNudge(sessionID, wake, inform, legID, completingJobID, "denied")
		// A denied wake leaves the active hold in place.
		l.Waits.parkForActiveHold(ctx, sessionID)
		return
	}
	if busy {
		if wake == anchor.WaitTimerFired {
			loopLogNudge(sessionID, wake, inform, legID, completingJobID, "drop:precedence_busy")
			return
		}
		loopLogNudge(sessionID, wake, inform, legID, completingJobID, "pending_busy")
		l.deferPromptWake(ctx, sessionID, pending)
		return
	}
	loopLogNudge(sessionID, wake, inform, legID, completingJobID, "queued_ready")
	if (wake == anchor.WorkerTaskFinished || wake == anchor.LegFinished) &&
		l.Cycles.workerCycleIdle(ctx, sessionID, completingJobID) {
		l.Waits.DisarmTimerBackstop(ctx, sessionID)
	}
	l.enqueuePending(sessionID, pending)
	l.schedulePendingDrain(ctx, sessionID, false)
}
func (l *Nudges) NudgeScanFinished(ctx context.Context, sessionID, scanID string, env anchor.Envelope) {
	l.Nudge(ctx, sessionID, anchor.ScanFinished, anchor.ScanFinished, scanID, env)
}
func (l *Nudges) NudgeProcessFinished(ctx context.Context, sessionID, handle string, env anchor.Envelope) {
	l.Nudge(ctx, sessionID, anchor.ProcessFinished, anchor.ProcessFinished, handle, env)
}
func (l *Nudges) NudgeProcessRefused(ctx context.Context, sessionID, handle string, env anchor.Envelope) {
	l.Nudge(ctx, sessionID, anchor.ProcessRefused, anchor.ProcessRefused, handle, env)
}
func (l *Nudges) NudgeWorkerBudgetRequested(ctx context.Context, sessionID, jobID string, env anchor.Envelope) {
	l.Nudge(ctx, sessionID, anchor.WorkerBudgetRequested, anchor.WorkerBudgetRequested, jobID, env)
}
func (l *Nudges) NudgeLegFinished(ctx context.Context, parentID string, completedAt time.Time, legID string) {
	env := anchor.Envelope{}
	if !completedAt.IsZero() {
		t := completedAt
		env.CompletedAt = &t
	}
	// The kick names the leg so a recall for omitted detail can address it.
	if id := strings.TrimSpace(legID); id != "" {
		env.Vars = map[string]any{"leg_id": id}
	}
	l.Nudge(ctx, parentID, anchor.LegFinished, anchor.LegFinished, legID, env)
}
func (l *Nudges) ClearPending(sessionID string) {
	if l == nil {
		return
	}
	l.pendingQueues.Delete(sessionID)
}
func (l *Nudges) DrainPending(ctx context.Context, sessionID string) {
	if l == nil {
		return
	}
	l.drainPending(context.WithoutCancel(ctx), sessionID, false)
}
func (l *Nudges) notifyLoopQuiescent(ctx context.Context, sessionID string) {
	if l == nil || (!l.Turns.hostTurnBlocked(ctx, sessionID) && (l.HasPendingLoopWakes(sessionID) || !l.Cycles.workerCycleIdle(ctx, sessionID, ""))) {
		return
	}
	if notify := l.loopDeps().OnLoopQuiescent; notify != nil {
		notify(ctx, sessionID)
	}
}
func (l *Nudges) recentKick(sessionID, runID, legID string, wake anchor.ID) bool {
	if strings.TrimSpace(runID) == "" {
		return false
	}
	key := loopKickKey{sessionID: sessionID, runID: runID, legID: legID, wake: wake}
	if v, ok := l.kickDedup.Load(key); ok {
		if stamp, ok := v.(loopKickStamp); ok && time.Since(stamp.at) < 30*time.Second {
			return true
		}
	}
	return false
}
func (l *Nudges) markKick(sessionID, runID, legID string, wake anchor.ID) {
	if strings.TrimSpace(runID) == "" {
		return
	}
	key := loopKickKey{sessionID: sessionID, runID: runID, legID: legID, wake: wake}
	l.kickDedup.Store(key, loopKickStamp{at: time.Now().UTC()})
}
func (l *Nudges) Pending(sessionID string) (anchor.ID, bool) {
	if l == nil {
		return "", false
	}
	pending, ok := l.sessionPendingQueue(sessionID).peek()
	return pending.wake, ok
}
