package coordinator

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectTurnTools resolves callable tool schemas, the compiled surface plan,
// and the active coordinator surface ID through the live PromptLoop pipeline.
func (r *Runtime) ProjectTurnTools(ctx context.Context, sess *api.Session, history []api.Message, frame inject.CoordinatorTurnFrame) ([]tools.ToolMeta, toolsurface.Plan, string, error) {
	r.depsMu.Lock()
	r.refreshDepsLocked()
	loop := *r.loop
	r.depsMu.Unlock()
	deps := loop.Context.Deps
	if sess != nil && !sess.IsWorkerChild() && frame.RunContext.RunID == "" && frame.Roster == nil && deps.CoordinatorFrame != nil {
		if built, err := deps.CoordinatorFrame.BuildCoordinatorTurnFrame(ctx, sess.ID, sess); err == nil {
			machine := frame.Machine
			frame = built
			frame.Machine = machine
		}
	}
	if sess != nil && !frame.Machine.Compiled() && deps.RefreshRoundMachine != nil {
		inject.StampMachine(&frame, deps.RefreshRoundMachine(ctx, sess, frame.Machine.ProfileID))
	}
	return promptloop.ProjectTurnTools(ctx, deps, sess, history, frame)
}

// ProjectPreparation shares the live request's schema projection.
func (r *Runtime) ProjectPreparation(ctx context.Context, sess *api.Session, history, messages []api.Message, frame inject.CoordinatorTurnFrame) (modelcall.CompletionRequest, error) {
	r.depsMu.Lock()
	r.refreshDepsLocked()
	loop := *r.loop
	r.depsMu.Unlock()
	return loop.ProjectPreparation(ctx, sess, history, messages, frame)
}
