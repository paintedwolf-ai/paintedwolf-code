package runtime

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectCoordinatorTurnFrame reads a candidate without changing review holds.
func (l *CoordinatorFrames) ProjectCoordinatorTurnFrame(ctx context.Context, sessionID string, sess *api.Session) (inject.CoordinatorTurnFrame, error) {
	if l == nil {
		return inject.CoordinatorTurnFrame{}, nil
	}
	projection := *l
	projection.Repairs = nil
	return projection.BuildCoordinatorTurnFrame(ctx, sessionID, sess)
}
