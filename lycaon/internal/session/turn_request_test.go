package session

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type requestWorkflowView struct {
	stubWorkflowManifest
	request ResolvedWorkflowRequest
	asked   *[]string
}

func (v requestWorkflowView) ResolvedRequest(_ context.Context, sessionID string) ResolvedWorkflowRequest {
	if v.asked != nil {
		*v.asked = append(*v.asked, sessionID)
	}
	return v.request
}

func TestTurnRequestDecidesAUserTurnForItsInstruction(t *testing.T) {
	mgr := NewManager(store.NewMemory(), nil, nil, settings.DefaultSessionLimits())
	opening, text, ok := mgr.turnRequest(t.Context(), PromptInput{Text: "fix the flaky test"}, nil, "u1", ResolvedWorkflowRequest{})
	if !ok || opening != "u1" || text != "fix the flaky test" {
		t.Fatalf("user turn = %q %q %v", opening, text, ok)
	}
}

func TestTurnRequestDecidesAParkedRequestOnItsFirstHostTurn(t *testing.T) {
	mgr := NewManager(store.NewMemory(), nil, nil, settings.DefaultSessionLimits())
	history := []api.Message{
		{ID: "slash", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "/security-survey"},
		{ID: "wake", Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Kind: api.MessageKindHostLoopWake, Visibility: api.MessageVisibilityInternal},
	}
	wake := PromptInput{HostSignal: &PromptHostSignal{Kind: api.MessageKindHostLoopWake}}
	request := ResolvedWorkflowRequest{OpeningMessageID: "slash", Text: "Review the project against its threat model."}
	opening, text, ok := mgr.turnRequest(t.Context(), wake, history, "", request)
	if !ok || opening != "slash" || text != request.Text {
		t.Fatalf("parked request = %q %q %v, want a decision for the run's resolved request", opening, text, ok)
	}
	request.Text = ""
	if _, text, ok := mgr.turnRequest(t.Context(), wake, history, "", request); !ok || text != "/security-survey" {
		t.Fatalf("without a resolved request = %q %v, want the message's own instruction", text, ok)
	}
}

func TestTurnRequestKeepsADecidedRequestAcrossHostTurns(t *testing.T) {
	st := store.NewMemory()
	mgr := NewManager(st, nil, nil, settings.DefaultSessionLimits())
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create session", err)
	_, err = st.PutTurnLoadReceipt(t.Context(), store.TurnLoadReceipt{SessionID: sess.ID, OpeningMessageID: "u1", Trigger: store.TurnLoadTriggerTurn})
	testutil.FailErr(t, "put receipt", err)
	history := []api.Message{{ID: "u1", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "add a flag"}}
	wake := PromptInput{HostSignal: &PromptHostSignal{Kind: api.MessageKindHostLoopWake}}
	if _, _, ok := mgr.turnRequest(t.Context(), wake, history, "", ResolvedWorkflowRequest{}); ok {
		t.Fatal("a host turn re-decided a request that already has a decision")
	}
}

func TestResolvedWorkflowRequestDoesNotReplaceWorkerAssignment(t *testing.T) {
	st := store.NewMemory()
	mgr := NewManager(st, nil, nil, settings.DefaultSessionLimits())
	var asked []string
	mgr.SetWorkflowSessionView(requestWorkflowView{request: ResolvedWorkflowRequest{Text: "run request"}, asked: &asked}, nil)
	root, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create root", err)
	worker, err := st.CreateChild(t.Context(), root, api.SpawnChildRequest{AgentType: "security-reviewer"})
	testutil.FailErr(t, "create worker", err)
	if request := mgr.resolvedWorkflowRequest(t.Context(), worker); request.Text != "" {
		t.Fatalf("worker inherited coordinator request %+v", request)
	}
	if len(asked) != 0 {
		t.Fatalf("worker consulted workflow request: %v", asked)
	}
	if request := mgr.resolvedWorkflowRequest(t.Context(), root); request.Text != "run request" {
		t.Fatalf("root request = %+v", request)
	}
	if len(asked) != 1 || asked[0] != root.ID {
		t.Fatalf("request sessions = %v", asked)
	}
}

func TestTurnRequestNeedsNoWorkflow(t *testing.T) {
	mgr := NewManager(store.NewMemory(), nil, nil, settings.DefaultSessionLimits())
	resolved := mgr.resolvedWorkflowRequest(t.Context(), &api.Session{ID: "s1"})
	opening, text, ok := mgr.turnRequest(t.Context(), PromptInput{Text: "inspect the logs"}, nil, "u1", resolved)
	if !ok || opening != "u1" || text != "inspect the logs" {
		t.Fatalf("request without workflow = %q %q %v", opening, text, ok)
	}
}

func TestTurnOptimizationRunsWithoutWorkflowOptIn(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		st := store.NewMemory()
		mgr := NewManager(st, nil, nil, settings.DefaultSessionLimits())
		sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
		testutil.FailErr(t, "create session", err)
		engine := &decidetest.Fake{Unavailable: unavailable}
		mgr.SetDecider(engine)
		mgr.SetTurnLoads(turnload.NewLedger())
		decision := mgr.beginTurnLoads(t.Context(), sess, sess.ID, PromptInput{Text: "inspect the logs"}, nil, "implement_investigate", "coordinator", "u1")
		if decision == nil {
			t.Fatal("ordinary turn skipped optimization without a workflow declaration")
		}
		if unavailable {
			if !decision.decision.Abstained || len(engine.Decisions) != 0 || len(mgr.OmittedUnits(sess.ID)) != 0 {
				t.Fatal("unavailable engine did not preserve guidance")
			}
		} else if len(engine.Decisions) == 0 {
			t.Fatal("available engine was not consulted")
		}
	}
}

func TestTurnOptimizationFailureRestoresGuidance(t *testing.T) {
	st := store.NewMemory()
	mgr := NewManager(st, nil, nil, settings.DefaultSessionLimits())
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create session", err)
	mgr.SetDecider(&decidetest.Fake{Err: errors.New("engine failed")})
	ledger := turnload.NewLedger()
	ledger.Restore(sess.ID, turnload.Standing{Omitted: []string{"survey-first-pass"}, Skill: &turnload.SkillPreload{Name: "old", Body: "old"}}, nil)
	mgr.SetTurnLoads(ledger)
	decision := mgr.beginTurnLoads(t.Context(), sess, sess.ID, PromptInput{Text: "inspect the logs"}, nil, "implement_investigate", "coordinator", "u1")
	if decision == nil || !decision.decision.Abstained || len(mgr.OmittedUnits(sess.ID)) != 0 || ledger.Preload(sess.ID) != nil {
		t.Fatalf("failed decision left stale prompt selection: %+v", decision)
	}
}

func TestWorkflowStartDecisionUsesItsBoundRequestOnce(t *testing.T) {
	st := store.NewMemory()
	mgr := NewManager(st, nil, nil, settings.DefaultSessionLimits())
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create session", err)
	request := ResolvedWorkflowRequest{RunID: "new-run", OpeningMessageID: "start", Text: "Review the project security."}
	wake := PromptInput{HostSignal: &PromptHostSignal{Kind: api.MessageKindHostLoopWake}}
	history := []api.Message{
		{ID: "old-user", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, WorkflowRunID: "old-run", Content: "Unrelated earlier work"},
		{ID: "start", Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, WorkflowRunID: "new-run", Kind: api.MessageKindWorkflowBoundary},
	}
	for _, messages := range [][]api.Message{history, history[:1], nil} {
		opening, text, ok := mgr.turnRequest(t.Context(), wake, messages, "", request)
		if !ok || opening != "start" || text != request.Text {
			t.Fatalf("workflow start = %q %q %v", opening, text, ok)
		}
	}
	_, err = st.PutTurnLoadReceipt(t.Context(), store.TurnLoadReceipt{SessionID: sess.ID, OpeningMessageID: "start", Trigger: store.TurnLoadTriggerTurn})
	testutil.FailErr(t, "record start decision", err)
	if _, _, ok := mgr.turnRequest(t.Context(), wake, history, "", request); ok {
		t.Fatal("workflow continuation repeated its start decision")
	}
	history = append(history, api.Message{ID: "follow-up", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, WorkflowRunID: "new-run", Content: "Focus on authentication"})
	opening, text, ok := mgr.turnRequest(t.Context(), wake, history, "", request)
	if !ok || opening != "follow-up" || text != "Focus on authentication" {
		t.Fatalf("follow-up = %q %q %v", opening, text, ok)
	}
}
