package promptloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

// TurnTools is the tool projection of one model request.
type TurnTools struct {
	ProfileID string
	// SurfaceID is empty outside coordinator profiles.
	SurfaceID string
	// Metas are the callable provider-projected tools.
	Metas []tools.ToolMeta
	Plan  toolsurface.Plan
	// DeferredSchemas are the provider schemas a load of each deferred tool offers.
	DeferredSchemas map[string]map[string]any
}

// Projection is the Reader input for a request that carries callable.
func (t TurnTools) Projection(callable []tools.ToolMeta) prompts.TurnProjection {
	offered := make([]string, 0, len(callable))
	schemas := make(map[string]map[string]any, len(callable)+len(t.DeferredSchemas))
	for name, schema := range t.DeferredSchemas {
		schemas[name] = schema
	}
	for _, meta := range callable {
		offered = append(offered, meta.Name)
		if meta.ArgsSchema != nil {
			schemas[meta.Name] = meta.ArgsSchema
		}
	}
	profileID := t.ProfileID
	if t.SurfaceID != "" {
		profileID = prompts.CoordinatorProfileID
	}
	return prompts.TurnProjection{
		ProfileID:        profileID,
		SurfaceID:        t.SurfaceID,
		Plan:             t.Plan,
		Offered:          offered,
		ProjectedSchemas: schemas,
	}
}

// ProjectTurnTools resolves callable tool schemas, the compiled surface plan,
// and the active coordinator surface ID through the live PromptLoop pipeline.
func ProjectTurnTools(
	ctx context.Context,
	deps ContextDeps,
	sess *api.Session,
	history []api.Message,
	frame inject.CoordinatorTurnFrame,
) (TurnTools, error) {
	pc := &promptContext{Deps: deps}
	return pc.coordinatorToolsForTurn(ctx, sess, turnProfileID(sess, frame), history, 0, 0, frame, nil)
}

// TurnReader projects the next request's tools and derives its Reader from the
// session's host facts.
func TurnReader(
	ctx context.Context,
	deps ContextDeps,
	sess *api.Session,
	history []api.Message,
	frame inject.CoordinatorTurnFrame,
) (prompts.Reader, error) {
	turn, err := ProjectTurnTools(ctx, deps, sess, history, frame)
	if err != nil {
		return prompts.Reader{}, err
	}
	pc := &promptContext{Deps: deps}
	return pc.requestReader(ctx, sess, turn, turn.Metas)
}

func turnProfileID(sess *api.Session, frame inject.CoordinatorTurnFrame) string {
	profileID := strings.TrimSpace(frame.Machine.ProfileID)
	if profileID == "" && !sess.IsWorkerChild() {
		profileID = prompts.CoordinatorProfileID
	}
	return profileID
}

// requestReader derives the Reader of a request carrying callable from turn.
func (l *promptContext) requestReader(ctx context.Context, sess *api.Session, turn TurnTools, callable []tools.ToolMeta) (prompts.Reader, error) {
	if l == nil || l.Deps.HostFacts == nil {
		return prompts.Reader{}, fmt.Errorf("session host facts not configured")
	}
	host, err := l.Deps.HostFacts(ctx, sess)
	if err != nil {
		return prompts.Reader{}, fmt.Errorf("session host facts: %w", err)
	}
	if !turn.Plan.Compiled() && l.Deps.Policy == nil {
		// A loop without a tool policy compiles no plan; its requests carry no tools.
		turn.Plan = toolsurface.Compile(nil, nil)
	}
	return prompts.NewReader(host, turn.Projection(callable))
}

// ProjectPreparation compiles callable schemas and their standing procedures without opening a turn.
func (l *PromptLoop) ProjectPreparation(ctx context.Context, sess *api.Session, history, messages []api.Message, frame inject.CoordinatorTurnFrame) (modelcall.CompletionRequest, error) {
	turn, err := ProjectTurnTools(ctx, l.Context.Deps, sess, history, frame)
	if err != nil {
		return modelcall.CompletionRequest{}, err
	}
	if len(turn.Metas) > 0 && l.Context.Deps.ToolProcedures != nil {
		reader, err := l.Context.requestReader(ctx, sess, turn, turn.Metas)
		if err != nil {
			return modelcall.CompletionRequest{}, err
		}
		messages, err = l.Tools.appendToolProcedures(ctx, sess, reader, messages)
		if err != nil {
			return modelcall.CompletionRequest{}, err
		}
	}
	return modelcall.CompletionRequest{Messages: messages, Tools: turn.Metas}, nil
}
