package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectPreparation compiles callable schemas and their standing procedures without opening a turn.
func (l *PromptLoop) ProjectPreparation(ctx context.Context, sess *api.Session, history, messages []api.Message, frame inject.CoordinatorTurnFrame) (modelcall.CompletionRequest, error) {
	profileID := frame.Machine.ProfileID
	metas, _, surfaceID, err := l.Context.coordinatorToolsForTurn(ctx, sess, profileID, history, "", 0, 0, frame, nil)
	if err != nil {
		return modelcall.CompletionRequest{}, err
	}
	if surfaceID != "" {
		profileID = surfaceID
	}
	messages, err = l.Tools.appendToolProcedures(ctx, sess, profileID, messages, metas)
	if err != nil {
		return modelcall.CompletionRequest{}, err
	}
	return modelcall.CompletionRequest{Messages: messages, Tools: metas}, nil
}
