package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectPreparation compiles callable schemas and their standing procedures without opening a turn.
func (l *PromptLoop) ProjectPreparation(ctx context.Context, sess *api.Session, history, messages []api.Message, frame inject.CoordinatorTurnFrame) (modelcall.CompletionRequest, error) {
	metas, _, _, err := l.Context.coordinatorToolsForTurn(ctx, sess, frame.Machine.ProfileID, history, "", 0, 0, frame, nil)
	if err != nil {
		return modelcall.CompletionRequest{}, err
	}
	messages, err = l.Tools.appendToolProcedures(ctx, sess, frame.Machine.ProfileID, messages, metas)
	if err != nil {
		return modelcall.CompletionRequest{}, err
	}
	return modelcall.CompletionRequest{Messages: messages, Tools: metas}, nil
}
