package assembly

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/pkg/api"
)

func (e *turnContextAssembler) appendExecutionState(ctx context.Context, sess *api.Session, coordinator bool, frame inject.CoordinatorTurnFrame, history []api.Message, turn *TurnAssemblyScratch, out []api.Message) []api.Message {
	if !coordinator || e.deps.Injects == nil {
		return out
	}
	_, state := e.surface.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	message := inject.RenderExecutionStateMessage(ctx, e.deps.Injects, sess.ID, history, state.VerifyCommand)
	if len(message.ContentParts) > 0 {
		out = append(out, message)
	}
	return out
}
