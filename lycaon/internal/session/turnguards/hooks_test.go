package turnguards

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolreach"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/closeoutevidence"
	"github.com/lycaon/lycaon/internal/session/closeouts"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	sessionverification "github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerrecords"
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

func (s *stubProgress) Get(context.Context, string) string {
	return s.content
}

type stubSessions struct {
	sess *api.Session
}

func (s stubSessions) Get(context.Context, string) (*api.Session, error) {
	return s.sess, nil
}

type stubSurface struct{}

func (stubSurface) PromptTurnSurfaceID(string) string { return "implement_routing" }

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

	ctx := tools.WithCallReach(t.Context(), toolreach.Offered([]string{"wait"}))
	_, _, err = svc.BeforeTool(ctx, sess, history, "", "wait", map[string]any{"timeout_ms": 60000})
	refusal, rejected := guidance.RefusalFromError(err)
	if !rejected || refusal.Code() != guard.WaitHiddenTextCode {
		t.Fatalf("expected %s refusal, got %v", guard.WaitHiddenTextCode, err)
	}
}

type unreadableLedger struct{ err error }

func (unreadableLedger) Get(context.Context, string) (*api.Session, error) { return nil, nil }
func (s unreadableLedger) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, s.err
}

func TestMidTurnEvidenceUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name string
		svc  *Service
		want bool
	}{
		{name: "no ledger wired", svc: &Service{}},
		{name: "no evidence service", svc: &Service{Verification: &sessionverification.Service{}}},
		{name: "readable", svc: &Service{Verification: &sessionverification.Service{Evidence: closeoutevidence.New(unreadableLedger{})}}},
		{name: "read fails", svc: &Service{Verification: &sessionverification.Service{Evidence: closeoutevidence.New(unreadableLedger{err: errors.New("offline")})}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.svc.midTurnEvidenceUnreadable(t.Context(), "coord-1", nil); got != tc.want {
				t.Fatalf("unreadable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBeforeFinishSkipsHostNoToolTurnWhenInvokeDisallowed(t *testing.T) {
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallBundled())
	loader, err := oar.NewLoader(filepath.Join("..", "..", "..", "..", "schemas"))
	testutil.FailErr(t, "oar.NewLoader", err)
	rs, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "oar.LoadEffectivePolicy", err)
	pipeline := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)

	policy := policyfacts.New(stubProfiles{})
	policy.SetSources(nil, nil, stubFeedback{}, nil)
	policy.SetPipeline(pipeline)

	sess := &api.Session{ID: "coord-1", Posture: api.SessionPostureBuild}
	state := stubState{state: surface.ImplementSessionState{WorkersInFlight: 1}}
	svc := New(stubSessions{sess: sess}, policy, nil, nil, state, nil, nil, nil, nil, closeouts.New(stubSessions{sess: sess}), nil, nil)

	reject, blocked := svc.BeforeFinish(
		t.Context(), sess, nil, "", "Final summary.", guidance.CloseoutDraft{}, surface.SurfaceImplementDispatch, false, nil, false,
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
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)

	policy := policyfacts.New(stubProfiles{})
	policy.SetSources(nil, nil, stubFeedback{}, nil)
	policy.SetPipeline(pipeline)

	sess := &api.Session{ID: "coord-1", Posture: api.SessionPostureBuild}
	store := stubSessions{sess: sess}
	state := stubState{state: surface.ImplementSessionState{
		ProgressOpenCount: 1,
	}}
	closeoutSvc := closeouts.New(store)
	progressStore := &stubProgress{content: "- [x] Done\n- [ ] Pending item\n"}
	svc := New(store, policy, nil, nil, state, nil, nil, nil, nil, closeoutSvc, nil, nil)
	svc.SetProgress(progressStore)

	history := []api.Message{
		{Role: api.MessageRoleUser, Kind: api.MessageKindHostLoopWake, Content: "worker completed"},
		{Role: api.MessageRoleAssistant, Content: "Final summary."},
	}
	for i := 0; i < synthesisDelayMaxPerPrompt; i++ {
		reject, blocked := svc.BeforeFinish(
			t.Context(), sess, history, "", "Final summary.", guidance.CloseoutDraft{}, "implement_routing", true, nil, true,
		)
		if !blocked || reject == nil || reject.Code() != progressOpenBeforeCloseoutCode {
			t.Fatalf("attempt %d: got blocked=%v reject=%v, want %s", i+1, blocked, reject, progressOpenBeforeCloseoutCode)
		}
	}
	reject, blocked := svc.BeforeFinish(
		t.Context(), sess, history, "", "Final summary.", guidance.CloseoutDraft{}, "implement_routing", true, nil, true,
	)
	if blocked || reject != nil {
		t.Fatalf("after %d retries: got blocked=%v reject=%v, want waiver", synthesisDelayMaxPerPrompt, blocked, reject)
	}

	closeoutSvc.BeginPrompt(t.Context(), sess, promptinput.Input{Text: "next user prompt"})
	reject, blocked = svc.BeforeFinish(
		t.Context(), sess, history, "", "Next turn summary.", guidance.CloseoutDraft{}, "implement_routing", true, nil, true,
	)
	if blocked || reject != nil {
		t.Fatalf("unchanged waived checklist on subsequent prompt: got blocked=%v reject=%v, want waiver preserved", blocked, reject)
	}

	progressStore.content = "- [x] Done\n- [ ] Newly added item\n"
	reject, blocked = svc.BeforeFinish(
		t.Context(), sess, history, "", "Updated checklist summary.", guidance.CloseoutDraft{}, "implement_routing", true, nil, true,
	)
	if !blocked || reject == nil || reject.Code() != progressOpenBeforeCloseoutCode {
		t.Fatalf("updated checklist after waiver: got blocked=%v reject=%v, want %s", blocked, reject, progressOpenBeforeCloseoutCode)
	}
}

type stubPlanCloseout struct {
	verdict workerplan.CloseoutVerdict
	err     error
}

func (s stubPlanCloseout) CheckSessionCloseout(context.Context, string, string, string) (workerplan.CloseoutVerdict, error) {
	return s.verdict, s.err
}

type stubWorkspace struct{ path string }

func (w stubWorkspace) ActivePath(context.Context, *api.Session) (string, error) { return w.path, nil }
func (w stubWorkspace) SettingsPath(context.Context, *api.Session) string        { return w.path }
func (w stubWorkspace) RootCount(context.Context, *api.Session) int              { return 1 }
func (w stubWorkspace) SettingsRoots(context.Context, *api.Session) []string     { return []string{w.path} }

func TestPlanCloseoutBlocksUnmetCriteriaAndAssertCompletion(t *testing.T) {
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallBundled())
	loader, err := oar.NewLoader(filepath.Join("..", "..", "..", "..", "schemas"))
	testutil.FailErr(t, "oar.NewLoader", err)
	rs, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "oar.LoadEffectivePolicy", err)
	pipeline := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)

	policy := policyfacts.New(stubProfiles{})
	policy.SetSources(nil, nil, stubFeedback{}, nil)
	policy.SetPipeline(pipeline)

	sess := &api.Session{ID: "coord-plan", ProjectID: "proj-1", Posture: api.SessionPostureBuild}
	store := stubSessions{sess: sess}
	state := stubState{state: surface.ImplementSessionState{}}
	svc := New(store, policy, nil, nil, state, nil, stubWorkspace{path: "/tmp/ws"}, nil, nil, closeouts.New(store), nil, nil)

	svc.SetPlanCloseout(stubPlanCloseout{verdict: workerplan.CloseoutVerdict{OK: true}})
	if reject, blocked := svc.PlanCloseout(t.Context(), sess, true, true); blocked || reject != nil {
		t.Fatalf("ok closeout blocked: %v", reject)
	}
	testutil.FailErr(t, "assert completion ok", svc.AssertCompletion(t.Context(), sess))

	svc.SetPlanCloseout(stubPlanCloseout{verdict: workerplan.CloseoutVerdict{
		Worker:   "api-builder",
		LegID:    "leg-1",
		ResultID: "res-1",
		Reason:   "missing test run",
		Outcomes: []workerrecords.CriterionOutcome{{Criterion: "tests_pass", Passed: false}},
	}})
	reject, blocked := svc.PlanCloseout(t.Context(), sess, true, false)
	if !blocked || reject == nil || !errors.Is(reject, guidance.ErrCloseoutStopped) {
		t.Fatalf("unmet criteria was not blocked: blocked=%v reject=%v", blocked, reject)
	}
	if err := svc.AssertCompletion(t.Context(), sess); err == nil {
		t.Fatal("AssertCompletion accepted unmet criteria")
	}

	wantErr := errors.New("closeout store offline")
	svc.SetPlanCloseout(stubPlanCloseout{err: wantErr})
	reject, blocked = svc.PlanCloseout(t.Context(), sess, true, true)
	if !blocked || reject == nil || !errors.Is(reject, wantErr) {
		t.Fatalf("closeout error not surfaced: blocked=%v reject=%v", blocked, reject)
	}
}
