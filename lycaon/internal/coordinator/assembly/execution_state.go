package assembly

import (
	"context"
	"log/slog"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/pkg/api"
)

func (e *turnContextAssembler) appendExecutionState(ctx context.Context, sess *api.Session, coordinator bool, frame inject.CoordinatorTurnFrame, history []api.Message, turn *TurnAssemblyScratch, out []api.Message) []api.Message {
	if !coordinator || e.deps.Injects == nil {
		return out
	}
	_, state := e.surface.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	window := history
	if e.deps.ReadMessageWindow != nil {
		var err error
		window, err = inject.ReadExecutionStateWindow(ctx, e.deps.ReadMessageWindow, sess.ID)
		if err != nil {
			slog.Warn("read execution state window", "session_id", sess.ID, "error", err)
			return out
		}
	}
	message := inject.RenderExecutionStateMessage(ctx, e.deps.Injects, sess.ID, window, state.VerifyCommand)
	if len(message.ContentParts) > 0 {
		out = append(out, message)
	}
	return out
}
