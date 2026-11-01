package coordinator

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectPreparationTools shares the live request's schema projection.
func (r *Runtime) ProjectPreparationTools(ctx context.Context, sess *api.Session, history []api.Message, frame inject.CoordinatorTurnFrame) ([]tools.ToolMeta, error) {
	r.depsMu.Lock()
	r.refreshDepsLocked()
	loop := *r.loop
	r.depsMu.Unlock()
	return loop.ProjectTools(ctx, sess, history, frame)
}
