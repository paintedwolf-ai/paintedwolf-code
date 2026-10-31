package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/oar"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type workerEventBoard struct{ projectID string }

func (b *workerEventBoard) BuildView(_ context.Context, projectID, _, sessionID string, level api.BoardDetailLevel) (api.BoardView, error) {
	b.projectID = projectID
	return api.BoardView{SessionID: sessionID, DetailLevel: level}, nil
}

func TestWorkerWritePublishesBoardByProjectIdentity(t *testing.T) {
	hub := events.NewMemoryHub()
	ch, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: testdbseed.DefaultProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to project events", err)
	defer unsubscribe()
	board := &workerEventBoard{}
	manager := NewHost(sessionstore.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	manager.SetEventPublisher(&events.Publisher{Hub: hub, Board: board})
	manager.Workers.Workspaces.AfterWorkerWrite(t.Context(), tools.ToolContext{Identity: tools.InvocationIdentity{ProjectID: testdbseed.DefaultProjectID, HandoffSessionID: "session"}}, "file.go")
	hub.FlushDebounced()
	if board.projectID != testdbseed.DefaultProjectID {
		t.Fatalf("board project = %q", board.projectID)
	}
	select {
	case event := <-ch:
		if event.Scope.ProjectID != testdbseed.DefaultProjectID || event.Topic != api.EventTopicBoard {
			t.Fatalf("board event = %+v", event)
		}
	default:
		t.Fatal("worker write did not refresh its project board")
	}
}

func TestStopWorkerRuntimePreservesParentAndSibling(t *testing.T) {
	mgr, _ := newTestManager(t)
	parent, err := mgr.Coordinator.Context.Sessions.(Store).Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create worker parent", err)
	child, err := mgr.Coordinator.Context.Sessions.(Store).CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create target worker", err)
	sibling, err := mgr.Coordinator.Context.Sessions.(Store).CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "code-reviewer"})
	testutil.FailErr(t, "create sibling worker", err)
	for _, id := range []string{parent.ID, child.ID, sibling.ID} {
		testutil.FailErr(t, "mark active runtime", mgr.Coordinator.Context.Sessions.(Store).SetSessionStatus(t.Context(), id, api.SessionStatusBusy))
	}
	released := []string{}
	testutil.FailErr(t, "register process cleanup", mgr.Resources.RegisterCleanup("fixture-processes", 20, func(_ context.Context, id string) error {
		released = append(released, id)
		return nil
	}))
	if err := mgr.Workers.Cancellations.StopRuntime(t.Context(), parent.ID); err == nil {
		t.Fatal("accepted coordinator as worker")
	}
	if len(released) != 0 {
		t.Fatalf("released parent resources: %v", released)
	}
	testutil.FailErr(t, "stop parked worker resources", mgr.Workers.Cancellations.StopRuntime(t.Context(), child.ID))
	if len(released) != 1 || released[0] != child.ID {
		t.Fatalf("released resources=%v", released)
	}
	for _, id := range []string{parent.ID, child.ID, sibling.ID} {
		got, err := mgr.Coordinator.Context.Sessions.(Store).Get(t.Context(), id)
		testutil.FailErr(t, "inspect retained session", err)
		want := api.SessionStatusBusy
		if id == child.ID {
			want = api.SessionStatusIdle
		}
		if got.Status != want {
			t.Fatalf("session=%s status=%s want=%s", id, got.Status, want)
		}
	}
}

func TestWorkflowDeliveryRequiresAcceptedCurrentPhaseCloseout(t *testing.T) {
	for _, tc := range []struct {
		name, run, phase                                         string
		internal, assembled, draft, sessionScope, toolCall, want bool
	}{
		{name: "current", run: "run", phase: "work", want: true},
		{name: "other run", run: "old-run", phase: "work"},
		{name: "earlier phase", run: "run", phase: "boot"},
		{name: "rejected draft", run: "run", phase: "work", internal: true},
		{name: "host grounded closeout", run: "run", phase: "work", assembled: true, want: true},
		{name: "host grounded draft", run: "run", phase: "work", assembled: true, draft: true},
		{name: "session report", run: "run", phase: "work", sessionScope: true},
		{name: "tool step", run: "run", phase: "work", toolCall: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, sess, _ := verifyGateHarness(t, "")
			ready, err := mgr.Runner.Transcript.DeliveredWorkflowPhase(t.Context(), sess.ID, "run", "work")
			testutil.FailErr(t, "read initial delivery state", err)
			if ready {
				t.Fatal("empty work phase completed before doing work")
			}
			msg := api.Message{ID: "report", Role: api.MessageRoleAssistant, Kind: api.MessageKindCompletionReport,
				Content: "Delivered, with validation limits.", WorkflowRunID: tc.run,
				Visibility: api.MessageVisibilityTranscript, CompletionReport: &api.CompletionReportMeta{Scope: api.CompletionReportScopePhase, Phase: tc.phase},
			}
			if tc.internal {
				msg.Visibility = api.MessageVisibilityInternal
			}
			if tc.assembled {
				msg.Grounding = &api.CitationGrounding{HostAssembled: true}
			}
			if tc.draft {
				msg.Kind = api.MessageKindDraft
				msg.CompletionReport = nil
			}
			if tc.sessionScope {
				msg.CompletionReport.Scope = api.CompletionReportScopeSession
			}
			if tc.toolCall {
				msg.ToolCalls = []api.ToolCall{{ID: "call", Name: "read"}}
			}
			testutil.FailErr(t, "append closeout", mgr.Coordinator.Context.Sessions.(Store).AppendMessages(t.Context(), sess.ID, msg))
			ready, err = mgr.Runner.Transcript.DeliveredWorkflowPhase(t.Context(), sess.ID, "run", "work")
			testutil.FailErr(t, "read reported delivery state", err)
			if ready != tc.want {
				t.Fatalf("ready=%v want %v", ready, tc.want)
			}
		})
	}
}

func workflowDomainFixture(v any) *WorkflowDomains {
	d := &WorkflowDomains{}
	d.Runs, _ = v.(WorkflowRuns)
	d.Policy, _ = v.(WorkflowPolicy)
	d.Ambient, _ = v.(WorkflowAmbient)
	d.Blueprints, _ = v.(WorkflowBlueprints)
	d.Batch, _ = v.(WorkflowBatch)
	d.Slash, _ = v.(WorkflowSlash)
	d.Requests, _ = v.(WorkflowRequests)
	d.Feedback, _ = v.(WorkflowFeedback)
	d.Transcript, _ = v.(WorkflowTranscript)
	d.Asks, _ = v.(WorkflowAsks)
	d.Fanout, _ = v.(WorkflowFanout)
	d.Phases, _ = v.(WorkflowPhases)
	d.Reports, _ = v.(WorkflowReports)
	d.Recovery, _ = v.(WorkflowRecovery)
	d.Cleanup, _ = v.(WorkflowCleanup)
	d.Reviews, _ = v.(WorkflowReviews)
	return d
}

type phaseGuardWorkflowView struct {
	stubWorkflowManifest
	state workflowfacts.WorkflowPhaseGuardState
}

func (v phaseGuardWorkflowView) ActivePhaseGuardState(context.Context, string) workflowfacts.WorkflowPhaseGuardState {
	return v.state
}

func TestCloseoutBlocksCompletionReportBeforeReportPhase(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(phaseGuardWorkflowView{state: workflowfacts.WorkflowPhaseGuardState{
		Phase:                 "ingest",
		ReportCloseoutPending: true,
	}}))

	reject, blocked := mgr.Coordinator.Guards.BeforeReportPhase(
		context.Background(), sess, `{"synthesis":"done"}`, "implement_synthesis", true,
	)
	if !blocked {
		t.Fatal("expected completion report to be blocked before report phase")
	}
	if body := reject.Error(); !strings.Contains(body, "WORKFLOW_REPORT_PHASE_REQUIRED_BEFORE_CLOSEOUT") || !strings.Contains(body, "ingest") {
		t.Fatalf("reject = %q", body)
	}
}

func TestCloseoutPhaseGuardAllowsInterimProseAndReportPhase(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(phaseGuardWorkflowView{state: workflowfacts.WorkflowPhaseGuardState{
		Phase:                 "ingest",
		ReportCloseoutPending: true,
	}}))
	if _, blocked := mgr.Coordinator.Guards.BeforeReportPhase(
		context.Background(), sess, "Still collecting scans.", "implement_synthesis", true,
	); blocked {
		t.Fatal("interim prose must not be treated as a completion report")
	}

	mgr.SetWorkflowDomains(workflowDomainFixture(phaseGuardWorkflowView{state: workflowfacts.WorkflowPhaseGuardState{Phase: "report"}}))
	if _, blocked := mgr.Coordinator.Guards.BeforeReportPhase(
		context.Background(), sess, `{"synthesis":"done"}`, "implement_synthesis", true,
	); blocked {
		t.Fatal("report phase must accept the completion report")
	}
}

func TestTaskObservationBlocksPendingPhaseObligation(t *testing.T) {
	sess := &api.Session{ID: "session-1", AgentType: "coordinator"}
	gc := &oar.GuardContext{}
	deps := workeradmission.WorkerCycleGuardDeps{PhaseGuardState: func(context.Context, string) workflowfacts.WorkflowPhaseGuardState {
		return workflowfacts.WorkflowPhaseGuardState{
			Phase: "ingest", PhaseObligationPending: true, PendingObligationKinds: []string{"scan"},
		}
	}}
	if err := workeradmission.ObserveCoordinatorTaskInFlight(context.Background(), deps, sess, "task", workeradmission.TaskIdentity{}, gc); err != nil {
		t.Fatalf("workeradmission.ObserveCoordinatorTaskInFlight: %v", err)
	}
	if !gc.Workflow.PhaseObligationPending || gc.Workflow.PhaseObligationKinds != "scan" {
		t.Fatal("phase obligation facts were not published")
	}
	if _, ok := gc.RejectData["WORKFLOW_OBLIGATION_PENDING"]; !ok {
		t.Fatalf("reject data = %#v", gc.RejectData)
	}
}

func TestTaskPolicyRejectsPendingPhaseObligation(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	deps := workeradmission.WorkerCycleGuardDeps{PhaseGuardState: func(context.Context, string) workflowfacts.WorkflowPhaseGuardState {
		return workflowfacts.WorkflowPhaseGuardState{
			Phase: "ingest", PhaseObligationPending: true, PendingObligationKinds: []string{"scan"},
		}
	}}
	reject, blocked, err := mgr.ToolPolicy.Block(
		context.Background(), oar.AnchorCoordinatorPreInvoke, sess, "task",
		map[string]any{"agent_type": "security-reviewer"},
		func(gc *oar.GuardContext) error {
			return workeradmission.ObserveCoordinatorTaskInFlight(context.Background(), deps, sess, "task", workeradmission.TaskIdentity{}, gc)
		},
	)
	if err != nil {
		t.Fatalf("tryOARBlock: %v", err)
	}
	if !blocked || reject == nil {
		t.Fatal("expected OAR to reject task while a phase obligation is pending")
	}
	if body := reject.Error(); !strings.Contains(body, "WORKFLOW_OBLIGATION_PENDING") || !strings.Contains(body, "host obligations (scan)") {
		t.Fatalf("reject = %q", body)
	}
}

type stubChildLookup map[string]*api.WorkerTask

func (s stubChildLookup) GetLatestByChildSessionID(_ context.Context, id string) (*api.WorkerTask, bool) {
	task, ok := s[id]
	return task, ok
}

func TestBeforeToolResolvesTaskIdentityAndRendersRejects(t *testing.T) {
	ctx := context.Background()
	mgr, sess := newSynthesisDelayManager(t)
	mgr.RewindRuntime.Progress.Set(sess.ID, "## Progress\n- [ ] active item")

	resolver := workeradmission.TaskIdentityResolver{
		Children: stubChildLookup{
			"child-ok": {
				AgentType:      "path-explorer",
				Scope:          &api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"**"}},
				WorkflowRunID:  "run-1",
				WorkflowPhase:  "build",
				WorkflowWorkID: "work-stale",
				MaxToolLoops:   12,
				BudgetRequest:  &api.WorkerBudgetRequest{RequestedMax: 20},
			},
		},
		WorkflowWork: func(_ context.Context, _, workID string) (spawn.WorkflowWork, bool, error) {
			if workID == "work-err" {
				return spawn.WorkflowWork{}, false, errors.New("workflow lookup failed")
			}
			if workID == "work-planned" {
				return spawn.WorkflowWork{
					RunID:        "run-1",
					Phase:        "build",
					AgentType:    "path-explorer",
					Scope:        &api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"**"}},
					MaxToolLoops: 10,
					Charter:      &api.WorkerTaskCharter{},
				}, true, nil
			}
			return spawn.WorkflowWork{}, false, nil
		},
		ToolBudget: func(string) spawn.WorkerToolBudget {
			return spawn.DefaultWorkerToolBudget()
		},
	}
	mgr.Coordinator.Guards.SetTaskIdentityResolver(resolver)

	// 1. Valid planned leg resolution through BeforeTool.
	if _, skip, err := mgr.Coordinator.Guards.BeforeTool(ctx, sess, nil, "", "task", map[string]any{"workflow_work_id": "work-planned"}); err != nil || skip {
		t.Fatalf("planned leg BeforeTool: skip=%v err=%v", skip, err)
	}
	// 2. Valid resumed child resolution with stale inherited workflow_work_id cleared.
	if _, skip, err := mgr.Coordinator.Guards.BeforeTool(ctx, sess, nil, "", "task", map[string]any{"child_session_id": "child-ok"}); err != nil || skip {
		t.Fatalf("resumed child BeforeTool: skip=%v err=%v", skip, err)
	}
	// 3. Non-ToolReject error from resolver (WorkflowWork error, bad max_tool_loops, bad scope).
	for _, badArgs := range []map[string]any{
		{"workflow_work_id": "work-err", "agent_type": "path-explorer"},
		{"agent_type": "path-explorer", "max_tool_loops": "not-an-int"},
		{"agent_type": "path-explorer", "scope": "not-an-object"},
	} {
		if _, _, err := mgr.Coordinator.Guards.BeforeTool(ctx, sess, nil, "", "task", badArgs); err == nil {
			t.Fatalf("expected error for args %#v", badArgs)
		}
	}
	// 4. Structured ToolRejects rendered via fallback formatter and via OAR BlockPlane.
	for _, rejectArgs := range []map[string]any{
		{},
		{"agent_type": "unknown-agent-profile"},
		{"agent_type": "path-explorer", "max_tool_loops": 999999},
		{"child_session_id": "missing-child"},
		{"child_session_id": "child-ok", "agent_type": "implementer"},
		{"child_session_id": "child-ok", "scope": map[string]any{"mode": "write", "paths": []any{"a.go"}}},
	} {
		if _, _, err := mgr.Coordinator.Guards.BeforeTool(ctx, sess, nil, "", "task", rejectArgs); err == nil {
			t.Fatalf("expected structured reject for args %#v", rejectArgs)
		}
	}
}
