package turnguards

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/closeouts"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubProfiles struct{}

func (stubProfiles) PromptToolProfile(context.Context, *api.Session) (string, error) {
	return "full", nil
}

type stubState struct {
	state surface.ImplementSessionState
}

func (s stubState) ForSession(context.Context, *api.Session) surface.ImplementSessionState {
	return s.state
}

type stubProgress struct {
	content string
}

func (s stubProgress) Get(context.Context, string) string {
	return s.content
}

type stubSessions struct {
	sess *api.Session
}

func (s stubSessions) Get(context.Context, string) (*api.Session, error) {
	return s.sess, nil
}

type stubSurface struct{}

func (stubSurface) PromptTurnSurfaceID(string) string { return "" }

type stubFeedback struct{}

func (stubFeedback) RenderResult(_ context.Context, _ string, res *oar.PipelineResult) (*guidance.Refusal, bool, error) {
	if res == nil || res.Decision == nil || res.Decision.Effect != oar.EffectBlock {
		return nil, false, nil
	}
	return guidance.NewRefusal(res.Decision.Code, res.Decision.Code), true, nil
}

func (stubFeedback) DeliverPostToolResult(_ context.Context, _ string, output string, _ *oar.PipelineResult) (string, guidance.ToolResultFacts) {
	return output, guidance.ToolResultFacts{}
}

func TestBeforeToolObservesWaitVisibility(t *testing.T) {
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallBundled())
	loader, err := oar.NewLoader(filepath.Join("..", "..", "..", "..", "schemas"))
	testutil.FailErr(t, "oar.NewLoader", err)
	rs, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "oar.LoadEffectivePolicy", err)
	pipeline := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorPreInvoke)

	policy := policyfacts.New(stubProfiles{})
	policy.SetSources(nil, nil, stubFeedback{}, nil)
	policy.SetPipeline(pipeline)

	svc := New(nil, policy, nil, nil, stubState{}, nil, nil, nil, nil, nil, nil, nil)
	svc.SetSurface(stubSurface{})

	sess := &api.Session{ID: "coord-1", Posture: api.SessionPostureBuild}
	history := []api.Message{{
		Role:      api.MessageRoleAssistant,
		Content:   "Still running",
		ToolCalls: []api.ToolCall{{ID: "wait-1", Name: "wait"}},
	}}

	ctx := tools.WithRecoveryTools(t.Context(), []string{"wait", "surface_note"})
	_, _, err = svc.BeforeTool(ctx, sess, history, "", "wait", map[string]any{"timeout_ms": 60000})
	refusal, rejected := guidance.RefusalFromError(err)
	if !rejected || refusal.Code() != guard.WaitHiddenTextCode {
		t.Fatalf("expected %s refusal, got %v", guard.WaitHiddenTextCode, err)
	}
}

func TestBeforeFinishSkipsHostNoToolTurnWhenInvokeDisallowed(t *testing.T) {
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallBundled())
	loader, err := oar.NewLoader(filepath.Join("..", "..", "..", "..", "schemas"))
	testutil.FailErr(t, "oar.NewLoader", err)
	rs, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "oar.LoadEffectivePolicy", err)
	pipeline := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorPreCloseout)

	policy := policyfacts.New(stubProfiles{})
	policy.SetSources(nil, nil, stubFeedback{}, nil)
	policy.SetPipeline(pipeline)

	sess := &api.Session{ID: "coord-1", Posture: api.SessionPostureBuild}
	state := stubState{state: surface.ImplementSessionState{WorkersInFlight: 1}}
	svc := New(stubSessions{sess: sess}, policy, nil, nil, state, nil, nil, nil, nil, closeouts.New(stubSessions{sess: sess}), nil, nil)

	reject, blocked := svc.BeforeFinish(
		t.Context(), sess, nil, "", "Final summary.", spawn.SurfaceImplementCycle, false, nil, false,
	)
	if blocked || reject != nil {
		t.Fatalf("BeforeFinish with invokeAllowed=false blocked: blocked=%v reject=%v", blocked, reject)
	}
}

func TestBeforeFinishPrioritizesOpenProgressAndWaivesAfterRetryCap(t *testing.T) {
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallBundled())
	loader, err := oar.NewLoader(filepath.Join("..", "..", "..", "..", "schemas"))
	testutil.FailErr(t, "oar.NewLoader", err)
	rs, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "oar.LoadEffectivePolicy", err)
	pipeline := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorPreCloseout)

	policy := policyfacts.New(stubProfiles{})
	policy.SetSources(nil, nil, stubFeedback{}, nil)
	policy.SetPipeline(pipeline)

	sess := &api.Session{ID: "coord-1", Posture: api.SessionPostureBuild}
	store := stubSessions{sess: sess}
	state := stubState{state: surface.ImplementSessionState{
		ProgressOpenCount: 1,
		WorkersCompleted:  1,
	}}
	svc := New(store, policy, nil, nil, state, nil, nil, nil, nil, closeouts.New(store), nil, nil)
	svc.SetProgress(stubProgress{content: "- [x] Done\n- [ ] Pending item\n"})

	history := []api.Message{
		{Role: api.MessageRoleUser, Kind: api.MessageKindHostLoopWake, Content: "worker completed"},
		{Role: api.MessageRoleAssistant, Content: "Final summary."},
	}
	for i := 0; i < synthesisDelayMaxPerPrompt; i++ {
		reject, blocked := svc.BeforeFinish(
			t.Context(), sess, history, "", "Final summary.", spawn.SurfaceImplementCycle, true, nil, true,
		)
		if !blocked || reject == nil || reject.Code() != progressOpenBeforeCloseoutCode {
			t.Fatalf("attempt %d: got blocked=%v reject=%v, want %s", i+1, blocked, reject, progressOpenBeforeCloseoutCode)
		}
	}
	reject, blocked := svc.BeforeFinish(
		t.Context(), sess, history, "", "Final summary.", spawn.SurfaceImplementCycle, true, nil, true,
	)
	if blocked || reject != nil {
		t.Fatalf("after %d retries: got blocked=%v reject=%v, want waiver", synthesisDelayMaxPerPrompt, blocked, reject)
	}
}
