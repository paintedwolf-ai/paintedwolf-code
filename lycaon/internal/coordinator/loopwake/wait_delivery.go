package loopwake

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
)

const (
	waitResumeRetryInitial = time.Second
	waitResumeRetryMaximum = 30 * time.Second
)

// WaitDelivery binds one settled lease to its admission acknowledgement.
type WaitDelivery struct {
	LeaseID   string
	Condition awaitstore.Condition
	Pending   func() bool
	Admitted  func() error
}

type waitWinner struct {
	LeaseID        string
	Condition      awaitstore.Condition
	deliveryActive atomic.Bool
	retryScheduled atomic.Bool
	retryAttempt   atomic.Uint32
}

func (l *LoopEngine) rememberWaitWinner(sessionID, leaseID string, winner awaitstore.Condition) {
	if l != nil && strings.TrimSpace(sessionID) != "" && strings.TrimSpace(winner.Kind) != "" {
		l.waitWinners.Store(sessionID, &waitWinner{LeaseID: strings.TrimSpace(leaseID), Condition: winner})
	}
}

func (l *LoopEngine) waitWinner(sessionID string) (*waitWinner, bool) {
	if l == nil {
		return nil, false
	}
	winner, ok := l.waitWinners.Load(strings.TrimSpace(sessionID))
	result, valid := winner.(*waitWinner)
	return result, ok && valid
}

func (l *LoopEngine) markWaitWinnerDelivered(ctx context.Context, sessionID string, winner *waitWinner) error {
	if winner == nil {
		return nil
	}
	if store := l.durableWaitStore(); store != nil && winner.LeaseID != "" {
		if err := store.MarkResumeDelivered(ctx, winner.LeaseID); err != nil {
			return err
		}
	}
	l.waitWinners.CompareAndDelete(strings.TrimSpace(sessionID), winner)
	return nil
}

func (l *LoopEngine) runWaitResumeAsync(ctx context.Context, sessionID string) {
	if l.PromptExecutionActive(sessionID) || l.hostTurnBlocked(ctx, sessionID) {
		return
	}
	if _, active := l.promptActive.Load(sessionID); active {
		return
	}
	l.spawnAsyncTurn(ctx, sessionID, func(ctx context.Context) {
		winner, ok := l.waitWinner(sessionID)
		if !ok {
			return
		}
		l.runWaitResume(ctx, sessionID, winner)
	})
}

func (l *LoopEngine) runWaitResume(ctx context.Context, sessionID string, winner *waitWinner) {
	if winner == nil || l.hostTurnBlocked(ctx, sessionID) || !l.sameWaitWinner(sessionID, winner) || !winner.deliveryActive.CompareAndSwap(false, true) {
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
	if l.sameWaitWinner(sessionID, winner) && !l.hostTurnBlocked(ctx, sessionID) {
		cause := "wait resume admission was not acknowledged"
		if err != nil {
			cause = err.Error()
		}
		l.scheduleWaitResumeRetry(ctx, sessionID, winner, cause)
	}
}

func (l *LoopEngine) sameWaitWinner(sessionID string, winner *waitWinner) bool {
	current, ok := l.waitWinner(sessionID)
	return ok && current == winner
}

func (l *LoopEngine) scheduleWaitResumeRetry(ctx context.Context, sessionID string, winner *waitWinner, cause string) {
	if winner == nil || !l.sameWaitWinner(sessionID, winner) || !winner.retryScheduled.CompareAndSwap(false, true) {
		return
	}
	attempt := winner.retryAttempt.Add(1)
	delay := waitResumeRetryDelay(attempt)
	slog.WarnContext(ctx, "wait resume delivery deferred", "session_id", sessionID, "lease_id", winner.LeaseID,
		"retry_attempt", attempt, "retry_in", delay, "cause", cause)
	l.spawnAsyncTurn(ctx, sessionID, func(ctx context.Context) {
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

func waitResumeRetryDelay(attempt uint32) time.Duration {
	delay := waitResumeRetryInitial
	for step := uint32(1); step < attempt && delay < waitResumeRetryMaximum; step++ {
		delay *= 2
		if delay >= waitResumeRetryMaximum {
			return waitResumeRetryMaximum
		}
	}
	return delay
}
