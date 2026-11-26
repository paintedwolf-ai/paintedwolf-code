package loopwake

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/actionoutcome"
	"github.com/lycaon/lycaon/pkg/api"
)

func (l *LoopEngine) ForgetSession(ctx context.Context, sessionID string) {
	if l == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	finish, owner := l.Nudges.lifetimes.retire(sessionID)
	if !owner {
		return
	}
	defer finish()
	l.Subscriptions.interruptParkedProgram(ctx, sessionID, false)
	l.Subscriptions.InterruptSession(ctx, sessionID)
	l.Deliveries.closeWinner(sessionID, api.WakeReasonSessionClosed)
	l.Turns.cancelAndDrainAsyncTurns(sessionID)
	l.Waits.ForgetSession(ctx, sessionID)
	l.Nudges.ForgetSession(sessionID)
	l.Turns.ForgetSession(sessionID)
	l.Admission.ForgetSession(sessionID)
	l.Observations.ForgetSession(sessionID)
	l.Deliveries.closeWinner(sessionID, api.WakeReasonSessionClosed)
	l.Deliveries.TakeEndedWaits(sessionID)
	l.Outcomes.Forget(sessionID)
}
func (l *WaitSubscriptions) InterruptSession(ctx context.Context, sessionID string) {
	if store := l.durableWaitStore(); store != nil {
		_ = store.InterruptSession(ctx, sessionID, "canceled")
	}
}
func (l *WaitDeliveries) closeWinner(sessionID string, reason api.WakeReason) {
	if value, ok := l.waitWinners.LoadAndDelete(sessionID); ok {
		value.(*waitWinner).handoff.Resolve(actionoutcome.WakeResult(reason))
	}
}
func (l *HostTurns) ForgetSession(sessionID string) { l.promptActive.Delete(sessionID) }
func (l *PromptObservations) ForgetSession(sessionID string) {
	l.promptWorkflow.Delete(sessionID)
}
func (l *Admission) ForgetSession(sessionID string) {
	l.promptExecution.Delete(sessionID)
	l.budget.Range(func(key, _ any) bool {
		if k, ok := key.(loopBudgetKey); ok && k.sessionID == sessionID {
			l.budget.Delete(key)
		}
		return true
	})
}
func (l *Nudges) ForgetSession(sessionID string) {
	l.ClearPending(sessionID, api.WakeReasonSessionClosed)
	l.pendingDrain.Delete(sessionID)
	l.kickDedup.Range(func(key, _ any) bool {
		if k, ok := key.(loopKickKey); ok && k.sessionID == sessionID {
			l.kickDedup.Delete(key)
		}
		return true
	})
}
func (l *Waits) ForgetSession(ctx context.Context, sessionID string) {
	for {
		value, ok := l.sleep.LoadAndDelete(sessionID)
		if !ok {
			break
		}
		if st, valid := value.(*sessionSleep); valid && st != nil {
			st.mu.Lock()
			timerDone := st.timerDone
			cancelSleepTimerLocked(st)
			st.waitTriggers = nil
			st.processHandles = nil
			st.workerHandles = nil
			closed, hadLease := closeWaitLeaseLocked(st, sessionID)
			st.mu.Unlock()
			if hadLease {
				l.publishWaitLease(ctx, closed)
			}
			if timerDone != nil {
				<-timerDone
			}
		}
	}
}
