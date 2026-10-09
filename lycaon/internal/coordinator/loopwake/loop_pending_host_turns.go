package loopwake

import (
	"context"
)

func (l *HostTurns) hostTurnBlocked(ctx context.Context, sessionID string) bool {
	blocked := l.loopDeps().HostTurnBlocked
	return blocked != nil && blocked(ctx, sessionID)
}
