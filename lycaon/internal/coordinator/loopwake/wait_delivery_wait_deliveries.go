package loopwake

import (
	"context"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/promptresult"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type WaitDeliveriesDeps struct {
	RunWaitResume func(ctx context.Context, sessionID string, delivery WaitDelivery) (*promptresult.Result, error)
}
type WaitDeliveries struct {
	depsMu        sync.RWMutex
	deps          WaitDeliveriesDeps
	waitWinners   sync.Map
	Admission     *Admission
	Turns         *HostTurns
	Subscriptions *WaitSubscriptions
}

func (l *WaitDeliveries) setDeps(deps WaitDeliveriesDeps) {
	l.depsMu.Lock()
	l.deps = deps
	l.depsMu.Unlock()
}
func (l *WaitDeliveries) loopDeps() WaitDeliveriesDeps {
	if l == nil {
		return WaitDeliveriesDeps{}
	}
	l.depsMu.RLock()
	defer l.depsMu.RUnlock()
	return l.deps
}
func (l *WaitDeliveries) rememberWaitWinner(sessionID, leaseID string, winner awaitstore.Condition) {
	if l != nil && strings.TrimSpace(sessionID) != "" && strings.TrimSpace(winner.Kind) != "" {
		l.waitWinners.Store(sessionID, &waitWinner{LeaseID: strings.TrimSpace(leaseID), Condition: winner})
	}
}
func (l *WaitDeliveries) waitWinner(sessionID string) (*waitWinner, bool) {
	if l == nil {
		return nil, false
	}
	winner, ok := l.waitWinners.Load(strings.TrimSpace(sessionID))
	result, valid := winner.(*waitWinner)
	return result, ok && valid
}
func (l *WaitDeliveries) markWaitWinnerDelivered(ctx context.Context, sessionID string, winner *waitWinner) error {
	if winner == nil {
		return nil
	}
	if store := l.Subscriptions.durableWaitStore(); store != nil && winner.LeaseID != "" {
		if err := store.MarkResumeDelivered(ctx, winner.LeaseID); err != nil {
			return err
		}
	}
	l.waitWinners.CompareAndDelete(strings.TrimSpace(sessionID), winner)
	return nil
}
func (l *WaitDeliveries) runWaitResumeAsync(ctx context.Context, sessionID string) {
	if l.Admission.PromptExecutionActive(sessionID) || l.Turns.hostTurnBlocked(ctx, sessionID) {
		return
	}
	if _, active := l.Turns.promptActive.Load(sessionID); active {
		return
	}
	l.Turns.spawnAsyncTurn(ctx, sessionID, func(ctx context.Context) {
		winner, ok := l.waitWinner(sessionID)
		if !ok {
			return
		}
		l.runWaitResume(ctx, sessionID, winner)
	})
}
func (l *WaitDeliveries) runWaitResume(ctx context.Context, sessionID string, winner *waitWinner) {
	if winner == nil || l.Turns.hostTurnBlocked(ctx, sessionID) || !l.sameWaitWinner(sessionID, winner) || !winner.deliveryActive.CompareAndSwap(false, true) {
		return
	}
	defer winner.deliveryActive.Store(false)
	deps := l.loopDeps()
	if deps.RunWaitResume == nil {
		l.scheduleWaitResumeRetry(ctx, sessionID, winner, "wait resume runner unavailable")
		return
	}
	_, err := deps.RunWaitResume(ctx, sessionID, WaitDelivery{
		LeaseID: winner.LeaseID, Condition: winner.Condition,
		Pending:  func() bool { return l.sameWaitWinner(sessionID, winner) },
		Admitted: func() error { return l.markWaitWinnerDelivered(ctx, sessionID, winner) },
	})
	if l.sameWaitWinner(sessionID, winner) && !l.Turns.hostTurnBlocked(ctx, sessionID) {
		cause := "wait resume admission was not acknowledged"
		if err != nil {
			cause = err.Error()
		}
		l.scheduleWaitResumeRetry(ctx, sessionID, winner, cause)
	}
}
func (l *WaitDeliveries) sameWaitWinner(sessionID string, winner *waitWinner) bool {
	current, ok := l.waitWinner(sessionID)
	return ok && current == winner
}
func (l *WaitDeliveries) scheduleWaitResumeRetry(ctx context.Context, sessionID string, winner *waitWinner, cause string) {
	if winner == nil || !l.sameWaitWinner(sessionID, winner) || !winner.retryScheduled.CompareAndSwap(false, true) {
		return
	}
	attempt := winner.retryAttempt.Add(1)
	delay := waitResumeRetryDelay(attempt)
	slog.WarnContext(ctx, "wait resume delivery deferred", "session_id", sessionID, "lease_id", winner.LeaseID,
		"retry_attempt", attempt, "retry_in", delay, "cause", cause)
	l.Turns.spawnAsyncTurn(ctx, sessionID, func(ctx context.Context) {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			winner.retryScheduled.Store(false)
			return
		case <-timer.C:
		}
		winner.retryScheduled.Store(false)
		l.runWaitResume(ctx, sessionID, winner)
	})
}
