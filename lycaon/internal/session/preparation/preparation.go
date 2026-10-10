package preparation

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/session/history"
	"github.com/lycaon/lycaon/internal/session/loading"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/sourcebrief"
	"github.com/lycaon/lycaon/internal/session/toolcontext"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type WorkerState interface {
	ForSession(context.Context, *api.Session) surface.ImplementSessionState
}
type WorkerWorkspace interface {
	Enrich(context.Context, *api.Session, tools.ToolContext) (tools.ToolContext, error)
}
type Service struct {
	History          *history.Service
	Loading          *loading.Service
	Profiles         *profiles.Service
	SourceBriefs     *sourcebrief.Service
	ToolContext      *toolcontext.Service
	Workspace        WorkerWorkspace
	Workers          WorkerState
	coordinatorFrame inject.CoordinatorTurnFrameSource
}

func New(history *history.Service, loading *loading.Service, profiles *profiles.Service, sourceBriefs *sourcebrief.Service, toolContext *toolcontext.Service, workspace WorkerWorkspace, workers WorkerState) *Service {
	return &Service{History: history, Loading: loading, Profiles: profiles, SourceBriefs: sourceBriefs, ToolContext: toolContext, Workspace: workspace, Workers: workers}
}
func (m *Service) SetFrame(frame inject.CoordinatorTurnFrameSource) { m.coordinatorFrame = frame }

type Assembly struct {
	History   []api.Message
	ProfileID string
	Machine   inject.Machine
	ToolCtx   tools.ToolContext
}

// Build prepares one prompt run; openingMessageID is the user
// message the turn just recorded, or empty for a continuation.
func (m *Service) Build(ctx context.Context, sess *api.Session, id string, in promptinput.Input, openingMessageID string) (Assembly, error) {
	var zero Assembly
	history, err := m.History.Load(ctx, sess)
	if err != nil {
		return zero, err
	}
	recorded, _ := m.Loading.RecordedStanding(ctx, id)
	m.Loading.Ledger.Restore(id, recorded, history)
	profileID := strings.TrimSpace(in.ToolProfile)
	if profileID == "" {
		profileID, err = m.Profiles.PromptToolProfile(ctx, sess)
		if err != nil {
			return zero, err
		}
	}
	surfaceID := m.Surface(ctx, sess, profileID, m.History.ApplyView(ctx, sess, history))
	history, _, err = m.History.Fit(ctx, sess, history, surfaceID)
	if err != nil {
		return zero, err
	}
	m.SourceBriefs.Record(ctx, sess, openingMessageID)
	// Select turn additions before constructing their tool context.
	decision := m.Loading.Begin(ctx, sess, id, in, history, surfaceID, profileID, openingMessageID)
	machine := m.Profiles.CompileMachine(ctx, sess, profileID)
	tctx, err := m.ToolContext.Build(ctx, sess, profileID, machine)
	if err != nil {
		return zero, err
	}
	tctx.Turn.TurnWritePinRootID = strings.TrimSpace(in.WritePinRootID)
	tctx.Turn.TurnWritePinGlobs = append([]string(nil), in.WritePinGlobs...)
	tctx, err = m.Workspace.Enrich(ctx, sess, tctx)
	if err != nil {
		return zero, err
	}
	m.Loading.Finish(ctx, sess, id, tctx, decision)
	return Assembly{
		History:   history,
		ProfileID: profileID,
		Machine:   machine,
		ToolCtx:   tctx,
	}, nil
}

func (m *Service) Surface(ctx context.Context, sess *api.Session, profileID string, history []api.Message) string {
	if m == nil || sess == nil || !guard.IsCoordinatorProfile(profileID) {
		return ""
	}
	var runCtx api.CoordinatorRunContext
	if m.coordinatorFrame != nil {
		if frame, err := m.coordinatorFrame.BuildCoordinatorTurnFrame(ctx, sess.ID, sess); err == nil {
			runCtx = frame.RunContext
		}
	}
	implState := m.Workers.ForSession(ctx, sess)
	return surface.ResolveTurnProfile(runCtx, sess, history, implState).SurfaceID
}
