package loopwake

import (
	"context"
	"strings"
)

func (l *LoopEngine) ForgetSession(ctx context.Context, sessionID string) {
	if l == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	l.Subscriptions.InterruptSession(ctx, sessionID)
	l.Deliveries.ForgetSession(sessionID)
	l.Turns.cancelAndDrainAsyncTurns(sessionID)
	l.Waits.ForgetSession(ctx, sessionID)
	l.Nudges.ForgetSession(sessionID)
	l.Turns.ForgetSession(sessionID)
	l.Admission.ForgetSession(sessionID)
	l.Observations.ForgetSession(sessionID)
	l.Deliveries.ForgetSession(sessionID)
}
func (l *WaitSubscriptions) InterruptSession(ctx context.Context, sessionID string) {
	if store := l.durableWaitStore(); store != nil {
		_ = store.InterruptSession(ctx, sessionID, "canceled")
	}
}
func (l *WaitDeliveries) ForgetSession(sessionID string) { l.waitWinners.Delete(sessionID) }
func (l *HostTurns) ForgetSession(sessionID string)      { l.promptActive.Delete(sessionID) }
func (l *PromptObservations) ForgetSession(sessionID string) {
	l.promptObservedSeq.Delete(sessionID)
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
	l.pendingQueues.Delete(sessionID)
	l.pendingDrain.Delete(sessionID)
	l.pendingWorkerQueues.Delete(sessionID)
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
