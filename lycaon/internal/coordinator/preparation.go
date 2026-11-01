package coordinator

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectPreparation shares the live request's schema projection.
func (r *Runtime) ProjectPreparation(ctx context.Context, sess *api.Session, history, messages []api.Message, frame inject.CoordinatorTurnFrame) (modelcall.CompletionRequest, error) {
	r.depsMu.Lock()
	r.refreshDepsLocked()
	loop := *r.loop
	r.depsMu.Unlock()
	return loop.ProjectPreparation(ctx, sess, history, messages, frame)
}
