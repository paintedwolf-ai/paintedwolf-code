package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type requestWorkflowView struct {
	stubWorkflowManifest
	request workflowfacts.ResolvedWorkflowRequest
	asked   *[]string
}

func (v requestWorkflowView) ResolvedRequest(_ context.Context, sessionID string) workflowfacts.ResolvedWorkflowRequest {
	if v.asked != nil {
		*v.asked = append(*v.asked, sessionID)
	}
	return v.request
}

func TestTurnRequestDecidesAUserTurnForItsInstruction(t *testing.T) {
	mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	opening, text, ok := mgr.Coordinator.Loading.Request(t.Context(), promptinput.Input{Text: "fix the flaky test"}, nil, "u1", workflowfacts.ResolvedWorkflowRequest{})
	if !ok || opening != "u1" || text != "fix the flaky test" {
		t.Fatalf("user turn = %q %q %v", opening, text, ok)
	}
}

func TestTurnRequestDecidesAParkedRequestOnItsFirstHostTurn(t *testing.T) {
	mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	history := []api.Message{
		{ID: "slash", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "/security-survey"},
		{ID: "wake", Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Kind: api.MessageKindHostLoopWake, Visibility: api.MessageVisibilityInternal},
	}
	wake := promptinput.Input{HostSignal: &promptinput.HostSignal{Kind: api.MessageKindHostLoopWake}}
	request := workflowfacts.ResolvedWorkflowRequest{OpeningMessageID: "slash", Text: "Review the project against its threat model."}
	opening, text, ok := mgr.Coordinator.Loading.Request(t.Context(), wake, history, "", request)
	if !ok || opening != "slash" || text != request.Text {
		t.Fatalf("parked request = %q %q %v, want a decision for the run's resolved request", opening, text, ok)
	}
	request.Text = ""
	if _, text, ok := mgr.Coordinator.Loading.Request(t.Context(), wake, history, "", request); !ok || text != "/security-survey" {
		t.Fatalf("without a resolved request = %q %v, want the message's own instruction", text, ok)
	}
}

func TestTurnRequestKeepsADecidedRequestAcrossHostTurns(t *testing.T) {
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create session", err)
	_, err = st.PutTurnLoadReceipt(t.Context(), store.TurnLoadReceipt{SessionID: sess.ID, OpeningMessageID: "u1", Trigger: store.TurnLoadTriggerTurn})
	testutil.FailErr(t, "put receipt", err)
	history := []api.Message{{ID: "u1", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "add a flag"}}
	wake := promptinput.Input{HostSignal: &promptinput.HostSignal{Kind: api.MessageKindHostLoopWake}}
	if _, _, ok := mgr.Coordinator.Loading.Request(t.Context(), wake, history, "", workflowfacts.ResolvedWorkflowRequest{}); ok {
		t.Fatal("a host turn re-decided a request that already has a decision")
	}
}

func TestResolvedWorkflowRequestDoesNotReplaceWorkerAssignment(t *testing.T) {
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	var asked []string
	mgr.SetWorkflowDomains(workflowDomainFixture(requestWorkflowView{request: workflowfacts.ResolvedWorkflowRequest{Text: "run request"}, asked: &asked}))
	root, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create root", err)
	worker, err := st.CreateChild(t.Context(), root, api.SpawnChildRequest{AgentType: "security-reviewer"})
	testutil.FailErr(t, "create worker", err)
	if request := mgr.Coordinator.Loading.ResolvedRequest(t.Context(), worker); request.Text != "" {
		t.Fatalf("worker inherited coordinator request %+v", request)
	}
	if len(asked) != 0 {
		t.Fatalf("worker consulted workflow request: %v", asked)
	}
	if request := mgr.Coordinator.Loading.ResolvedRequest(t.Context(), root); request.Text != "run request" {
		t.Fatalf("root request = %+v", request)
	}
	if len(asked) != 1 || asked[0] != root.ID {
		t.Fatalf("request sessions = %v", asked)
	}
}

func TestTurnRequestNeedsNoWorkflow(t *testing.T) {
	mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	resolved := mgr.Coordinator.Loading.ResolvedRequest(t.Context(), &api.Session{ID: "s1"})
	opening, text, ok := mgr.Coordinator.Loading.Request(t.Context(), promptinput.Input{Text: "inspect the logs"}, nil, "u1", resolved)
	if !ok || opening != "u1" || text != "inspect the logs" {
		t.Fatalf("request without workflow = %q %q %v", opening, text, ok)
	}
}

func TestTurnOptimizationRunsWithoutWorkflowOptIn(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		st := store.NewMemory()
		mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
		sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
		testutil.FailErr(t, "create session", err)
		engine := &decidetest.Fake{Unavailable: unavailable}
		mgr.Coordinator.Loading.SetDecider(engine)
		mgr.Coordinator.Loading.SetLedger(turnload.NewLedger())
		decision := mgr.Coordinator.Loading.Begin(t.Context(), sess, sess.ID, promptinput.Input{Text: "inspect the logs"}, nil, "implement_investigate", "coordinator", "u1")
		if decision == nil {
			t.Fatal("ordinary turn skipped optimization without a workflow declaration")
		}
		mgr.Coordinator.Loading.Finish(t.Context(), sess, sess.ID, tools.ToolContext{}, decision)
		receipt, found, readErr := st.LatestTurnLoadReceipt(t.Context(), sess.ID)
		testutil.FailErr(t, "read turn decision", readErr)
		if !found {
			t.Fatal("decision receipt missing")
		}
		if unavailable {
			if !receipt.Abstained || len(engine.Decisions) != 0 || len(mgr.Coordinator.Loading.OmittedUnits(sess.ID)) != 0 {
				t.Fatal("unavailable engine did not preserve guidance")
			}
		} else if len(engine.Decisions) == 0 {
			t.Fatal("available engine was not consulted")
		}
	}
}

func TestTurnOptimizationFailureRestoresGuidance(t *testing.T) {
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create session", err)
	mgr.Coordinator.Loading.SetDecider(&decidetest.Fake{Err: errors.New("engine failed")})
	ledger := turnload.NewLedger()
	ledger.Restore(sess.ID, turnload.Standing{Omitted: []string{"survey-first-pass"}, Skill: &turnload.SkillPreload{Name: "old", Body: "old"}}, nil)
	mgr.Coordinator.Loading.SetLedger(ledger)
	mgr.Coordinator.Nudges.SetLedger(ledger)
	decision := mgr.Coordinator.Loading.Begin(t.Context(), sess, sess.ID, promptinput.Input{Text: "inspect the logs"}, nil, "implement_investigate", "coordinator", "u1")
	if decision == nil {
		t.Fatal("failed engine skipped decision receipt")
	}
	mgr.Coordinator.Loading.Finish(t.Context(), sess, sess.ID, tools.ToolContext{}, decision)
	receipt, found, err := st.LatestTurnLoadReceipt(t.Context(), sess.ID)
	testutil.FailErr(t, "read failed decision", err)
	if !found || !receipt.Abstained || len(mgr.Coordinator.Loading.OmittedUnits(sess.ID)) != 0 || ledger.Preload(sess.ID) != nil {
		t.Fatalf("failed decision left stale prompt selection: %+v", decision)
	}
}

func TestWorkflowStartDecisionUsesItsBoundRequestOnce(t *testing.T) {
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create session", err)
	request := workflowfacts.ResolvedWorkflowRequest{RunID: "new-run", OpeningMessageID: "start", Text: "Review the project security."}
	wake := promptinput.Input{HostSignal: &promptinput.HostSignal{Kind: api.MessageKindHostLoopWake}}
	history := []api.Message{
		{ID: "old-user", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, WorkflowRunID: "old-run", Content: "Unrelated earlier work"},
		{ID: "start", Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, WorkflowRunID: "new-run", Kind: api.MessageKindWorkflowBoundary},
	}
	for _, messages := range [][]api.Message{history, history[:1], nil} {
		opening, text, ok := mgr.Coordinator.Loading.Request(t.Context(), wake, messages, "", request)
		if !ok || opening != "start" || text != request.Text {
			t.Fatalf("workflow start = %q %q %v", opening, text, ok)
		}
	}
	_, err = st.PutTurnLoadReceipt(t.Context(), store.TurnLoadReceipt{SessionID: sess.ID, OpeningMessageID: "start", Trigger: store.TurnLoadTriggerTurn})
	testutil.FailErr(t, "record start decision", err)
	if _, _, ok := mgr.Coordinator.Loading.Request(t.Context(), wake, history, "", request); ok {
		t.Fatal("workflow continuation repeated its start decision")
	}
	history = append(history, api.Message{ID: "follow-up", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, WorkflowRunID: "new-run", Content: "Focus on authentication"})
	opening, text, ok := mgr.Coordinator.Loading.Request(t.Context(), wake, history, "", request)
	if !ok || opening != "follow-up" || text != "Focus on authentication" {
		t.Fatalf("follow-up = %q %q %v", opening, text, ok)
	}
}

const evidenceBoundarySecret = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"

// evidenceScreenRedactor stands in for the durable secret screen.
func evidenceScreenRedactor(_ context.Context, msg api.Message) (api.Message, bool) {
	out := msg
	out.Content = strings.ReplaceAll(msg.Content, evidenceBoundarySecret, "{{paintedwolf-secret:ci_token}}")
	return out, out.Content != msg.Content
}

// evidenceFileBytes reads every verify evidence row this project wrote.
func evidenceFileBytes(t *testing.T, m *Host, sess *api.Session) []byte {
	t.Helper()
	root := m.Verification.EvidenceRootFor(sess)
	if strings.TrimSpace(root) == "" {
		t.Fatal("evidence root is unset")
	}
	var all []byte
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return walkErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		all = append(all, data...)
		return nil
	})
	testutil.FailErr(t, "walk evidence dir", err)
	if len(all) == 0 {
		t.Fatal("no evidence rows were written")
	}
	return all
}

func TestSourceRunEvidenceIsScreenedBeforeItIsPersisted(t *testing.T) {
	mgr, sess, _ := verifyGateHarness(t, "")
	mgr.Runner.Transcript.SetRedactor(evidenceScreenRedactor)
	recordVerify(t, mgr, sess, "deploy --token "+evidenceBoundarySecret, 0)

	rows := evidenceFileBytes(t, mgr, sess)
	if strings.Contains(string(rows), evidenceBoundarySecret) {
		t.Fatalf("raw credential reached the evidence file: %s", rows)
	}
	if !strings.Contains(string(rows), "{{paintedwolf-secret:ci_token}}") {
		t.Fatalf("evidence row did not keep the reference-bearing command: %s", rows)
	}
}

func TestScreenedEvidenceStillMatchesTheDeclaredCommand(t *testing.T) {
	mgr, sess, history := verifyGateHarness(t, "make test")
	mgr.Runner.Transcript.SetRedactor(evidenceScreenRedactor)
	recordVerify(t, mgr, sess, "make test", 0)

	passed, _, _ := mgr.Verification.GateState(context.Background(), sess, workSince(history))
	if !passed {
		t.Fatal("declared verify command no longer satisfies the gate after screening")
	}
}

func TestWaitResumeAcquiresItsOwnDispatchLane(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "pending"
		if canceled {
			name = "interrupted"
		}
		t.Run(name, func(t *testing.T) {
			manager, sessions := newReceiptTestManager(t)
			session := newReceiptTestSession(t, manager, sessions)
			ctx, release := manager.Submissions.LockDispatch(t.Context(), session.ID)
			defer func() { release() }()
			started := make(chan struct{})
			admitted := make(chan struct{}, 1)
			finished := make(chan error, 1)
			leaseID := uuid.NewString()
			pending := true
			go func() {
				close(started)
				_, err := manager.Submissions.WaitResume(context.WithoutCancel(ctx), session.ID, loopwake.WaitDelivery{
					LeaseID: leaseID, Condition: awaitstore.Condition{Kind: "timer", Outcome: "timed_out"},
					Pending: func() bool { return pending }, Admitted: func() error { admitted <- struct{}{}; return nil },
				})
				finished <- err
			}()
			<-started
			runtime.Gosched()
			select {
			case <-admitted:
				t.Error("wait delivery inherited another execution's dispatch ownership")
			default:
			}
			pending = !canceled
			release()
			release = func() {}
			select {
			case err := <-finished:
				testutil.FailErr(t, "deliver wait after dispatch release", err)
			case <-t.Context().Done():
				t.Fatal("wait delivery did not acquire the released lane")
			}
			ids := sessions.admitted()
			if canceled && len(ids) != 0 {
				t.Fatalf("interrupted wait admitted receipts: %v", ids)
			}
			if !canceled && (len(ids) != 1 || ids[0] != leaseID) {
				t.Fatalf("wait admission identities = %v", ids)
			}
		})
	}
}

func TestConsumedStartupWakeKeepsWaitingUserTurnBusy(t *testing.T) {
	ctx := t.Context()
	memory := store.NewMemory()
	client := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "unexpected follow-up"}}}))
	mgr := NewHost(memory, Models{Client: client, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	sess, err := memory.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "start visible turn", memory.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
	loopWorkflowFixture1 := &settlementWorkflowSource{active: &api.WorkflowRun{
		ID: "run", Revision: 5, Status: api.WorkflowRunStatusRunning, CurrentPhase: "work",
	}}
	mgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: loopWorkflowFixture1, Approvals: loopWorkflowFixture1, Obligations: loopWorkflowFixture1})
	loop := mgr.Coordinator.Runtime.CoordinatorLoop()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), sess.ID) })
	finish := loop.Admission.BeginPromptExecution(ctx, sess.ID)
	observe := mgr.Runner.Coordinator.PromptLoop().Context.Deps.ObservePrompt(sess.ID)
	loop.Nudges.Nudge(ctx, sess.ID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
	observe(inject.CoordinatorTurnFrame{WorkflowRevision: 5, RunContext: api.CoordinatorRunContext{RunID: "run"}})
	loop.Waits.EnterSleep(ctx, sess.ID, time.Now().Add(time.Hour), "command", []loopwake.WaitTrigger{loopwake.WaitTriggerTimer, loopwake.WaitTriggerProcessDone}, []string{"process"}, loopwake.SleepMoverHost)
	loop.Waits.MarkWaitCalled(sess.ID)
	testutil.FailErr(t, "finish parked prompt", mgr.Runner.Settlement.Finish(ctx, sess.ID, false, false, ""))
	finish()
	loop.Turns.WaitForAsyncTurns(t.Context())
	testutil.FailErr(t, "settle drained wakes", mgr.Runner.Settlement.Drain(ctx, sess.ID))
	current, err := memory.Get(ctx, sess.ID)
	testutil.FailErr(t, "read waiting session", err)
	if current.Status != api.SessionStatusBusy || !loop.Waits.IsSleeping(sess.ID) || len(client.AllRequests()) != 0 {
		t.Fatalf("status=%s sleeping=%v prompts=%d", current.Status, loop.Waits.IsSleeping(sess.ID), len(client.AllRequests()))
	}
}
