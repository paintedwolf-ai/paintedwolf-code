package toolpolicy

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
)

type coordinatorTurnFrameContextKey struct{}

// WithCoordinatorTurnFrame binds one coordinator frame to a request.
func WithCoordinatorTurnFrame(ctx context.Context, frame *inject.CoordinatorTurnFrame) context.Context {
	return context.WithValue(ctx, coordinatorTurnFrameContextKey{}, frame)
}

func coordinatorTurnFrameFromContext(ctx context.Context) (*inject.CoordinatorTurnFrame, bool) {
	frame, ok := ctx.Value(coordinatorTurnFrameContextKey{}).(*inject.CoordinatorTurnFrame)
	return frame, ok && frame != nil
}

func coordinatorTurnFrameProjectRootCount(ctx context.Context) (int, bool) {
	frame, ok := coordinatorTurnFrameFromContext(ctx)
	if !ok {
		return 0, false
	}
	return frame.ProjectRootCount, true
}
