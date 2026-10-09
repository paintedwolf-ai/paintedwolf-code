package loopwake

import (
	"context"
)

func (l *Waits) rearmSleepAfterSkip(ctx context.Context, sessionID string) {
	if l == nil {
		return
	}
	until, _ := l.ResolveWaitUntil(ctx, sessionID, true, 0)
	// A re-arm continues the same wait, so it inherits who ends it and whether completion alone ends it.
	l.enterSleep(ctx, sessionID, sleepArm{
		until: until, untilComplete: l.Waits.activeUntilComplete(sessionID), reason: "skip:re-arm",
		triggers: l.Waits.Triggers(sessionID), processHandles: l.Waits.ActiveProcessHandles(sessionID),
		mover: l.Waits.activeSleepMover(sessionID),
	})
}
