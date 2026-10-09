package loopwake

import (
	"context"
	"strings"
)

func (l *Waits) publishWaitLease(ctx context.Context, lease WaitLease) {
	if l == nil || strings.TrimSpace(lease.ActivityID) == "" {
		return
	}
	publish := l.loopDeps().PublishWaitLease
	if publish == nil {
		return
	}
	publish(ctx, lease.SessionID, lease)
}
