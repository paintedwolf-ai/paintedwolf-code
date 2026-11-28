package coordinator

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectTurnTools resolves callable tool schemas, the compiled surface plan,
// and the active coordinator surface ID through the live PromptLoop pipeline.
func (r *Runtime) ProjectTurnTools(ctx context.Context, sess *api.Session, history []api.Message, frame inject.CoordinatorTurnFrame) (promptloop.TurnTools, error) {
	deps, frame := r.turnProjectionInputs(ctx, sess, frame)
	return promptloop.ProjectTurnTools(ctx, deps, sess, history, frame)
}

// TurnReader projects sess's next request through the live PromptLoop and
// derives its Reader from the session's host facts.
func (r *Runtime) TurnReader(ctx context.Context, sess *api.Session, history []api.Message, frame inject.CoordinatorTurnFrame) (prompts.Reader, error) {
	if r == nil {
		return prompts.Reader{}, errRuntimeNotConfigured
	}
	deps, frame := r.turnProjectionInputs(ctx, sess, frame)
	return promptloop.TurnReader(ctx, deps, sess, history, frame)
}

func (r *Runtime) turnProjectionInputs(ctx context.Context, sess *api.Session, frame inject.CoordinatorTurnFrame) (promptloop.ContextDeps, inject.CoordinatorTurnFrame) {
	r.depsMu.Lock()
	r.refreshDepsLocked()
	var deps promptloop.ContextDeps
	if r.loop != nil {
		deps = r.loop.Context.Deps
	}
	r.depsMu.Unlock()
	return deps, projectionFrame(ctx, deps, sess, frame)
}

// projectionFrame completes frame the way a live turn does before it projects
// tools: the coordinator run frame, then the round machine.
func projectionFrame(ctx context.Context, deps promptloop.ContextDeps, sess *api.Session, frame inject.CoordinatorTurnFrame) inject.CoordinatorTurnFrame {
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
	return frame
}

// turnReaderFor binds deps' projection as an assembly reader source.
func turnReaderFor(deps promptloop.ContextDeps) func(context.Context, *api.Session, []api.Message, inject.CoordinatorTurnFrame) (prompts.Reader, error) {
	return func(ctx context.Context, sess *api.Session, history []api.Message, frame inject.CoordinatorTurnFrame) (prompts.Reader, error) {
		return promptloop.TurnReader(ctx, deps, sess, history, projectionFrame(ctx, deps, sess, frame))
	}
}

// ProjectPreparation shares the live request's schema projection.
func (r *Runtime) ProjectPreparation(ctx context.Context, sess *api.Session, history, messages []api.Message, frame inject.CoordinatorTurnFrame) (modelcall.CompletionRequest, error) {
	r.depsMu.Lock()
	r.refreshDepsLocked()
	loop := *r.loop
	r.depsMu.Unlock()
	return loop.ProjectPreparation(ctx, sess, history, messages, frame)
}
