package promptloop

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectTurnTools resolves callable tool schemas, the compiled surface plan,
// and the active coordinator surface ID through the live PromptLoop pipeline.
func ProjectTurnTools(
	ctx context.Context,
	deps ContextDeps,
	sess *api.Session,
	history []api.Message,
	frame inject.CoordinatorTurnFrame,
) ([]tools.ToolMeta, toolsurface.Plan, string, error) {
	profileID := strings.TrimSpace(frame.Machine.ProfileID)
	if profileID == "" && !sess.IsWorkerChild() {
		profileID = prompts.CoordinatorProfileID
	}
	pc := &promptContext{Deps: deps}
	return pc.coordinatorToolsForTurn(ctx, sess, profileID, history, 0, 0, frame, nil)
}

// ProjectPreparation compiles callable schemas and their standing procedures without opening a turn.
func (l *PromptLoop) ProjectPreparation(ctx context.Context, sess *api.Session, history, messages []api.Message, frame inject.CoordinatorTurnFrame) (modelcall.CompletionRequest, error) {
	metas, _, surfaceID, err := ProjectTurnTools(ctx, l.Context.Deps, sess, history, frame)
	if err != nil {
		return modelcall.CompletionRequest{}, err
	}
	profileID := frame.Machine.ProfileID
	if surfaceID != "" {
		profileID = surfaceID
	}
	messages, err = l.Tools.appendToolProcedures(ctx, sess, profileID, messages, metas)
	if err != nil {
		return modelcall.CompletionRequest{}, err
	}
	return modelcall.CompletionRequest{Messages: messages, Tools: metas}, nil
}
