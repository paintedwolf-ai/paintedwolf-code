package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
)

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
