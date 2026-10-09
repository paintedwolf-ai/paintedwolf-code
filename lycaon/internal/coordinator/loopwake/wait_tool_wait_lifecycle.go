package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"strings"
	"time"
)

func (l *Waits) ParkForPendingUserInput(ctx context.Context, sessionID, reason string) {
	if l == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !l.Facts.sessionHasPendingUserInput(ctx, sessionID) {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "waiting for user ask"
	}
	until := l.Facts.pendingUserInputWaitDeadline(ctx, sessionID)
	// The timer bounds the park; user input ends it.
	l.EnterSleep(ctx, sessionID, until, reason, []WaitTrigger{WaitTriggerTimer}, nil, SleepMoverUser)
	l.MarkWaitCalled(sessionID)
}

func (l *Waits) ParkForHostObligation(ctx context.Context, sessionID string) {
	if l == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !l.Facts.sessionHostObligationHeld(ctx, sessionID) {
		return
	}
	overlayPromoteDue := l.Subscriptions.overlayPromoteDue(ctx, sessionID, anchor.Envelope{})
	l.EnterSleep(
		ctx,
		sessionID,
		time.Now().UTC().Add(l.Facts.sessionLimits(ctx, sessionID).CoordinatorMaxSleep()),
		l.Facts.hostObligationParkReason(ctx, sessionID),
		HostObligationWaitTriggers(overlayPromoteDue),
		nil,
		SleepMoverHost,
	)
	l.MarkWaitCalled(sessionID)
}
