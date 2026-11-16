package toolpolicy

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
)

type (
	coordinatorTurnFrameContextKey struct{}
	coordinatorSurfaceContextKey   struct{}
)

// WithCoordinatorTurnFrame binds one coordinator frame to a request.
func WithCoordinatorTurnFrame(ctx context.Context, frame *inject.CoordinatorTurnFrame) context.Context {
	return context.WithValue(ctx, coordinatorTurnFrameContextKey{}, frame)
}

// WithCoordinatorSurface binds the active coordinator surface ID to a tool listing context.
func WithCoordinatorSurface(ctx context.Context, surfaceID string) context.Context {
	surfaceID = strings.TrimSpace(surfaceID)
	if surfaceID == "" {
		return ctx
	}
	return context.WithValue(ctx, coordinatorSurfaceContextKey{}, surfaceID)
}

func coordinatorSurfaceFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	surfaceID, _ := ctx.Value(coordinatorSurfaceContextKey{}).(string)
	return strings.TrimSpace(surfaceID)
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
