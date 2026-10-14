package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

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
