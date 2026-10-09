package session

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
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
	mgr.SetWorkflowSessionView(phaseGuardWorkflowView{state: workflowfacts.WorkflowPhaseGuardState{
		Phase:                 "ingest",
		ReportCloseoutPending: true,
	}})

	reject, blocked := mgr.Guards.BeforeReportPhase(
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
	mgr.SetWorkflowSessionView(phaseGuardWorkflowView{state: workflowfacts.WorkflowPhaseGuardState{
		Phase:                 "ingest",
		ReportCloseoutPending: true,
	}})
	if _, blocked := mgr.Guards.BeforeReportPhase(
		context.Background(), sess, "Still collecting scans.", "implement_synthesis", true,
	); blocked {
		t.Fatal("interim prose must not be treated as a completion report")
	}

	mgr.SetWorkflowSessionView(phaseGuardWorkflowView{state: workflowfacts.WorkflowPhaseGuardState{Phase: "report"}})
	if _, blocked := mgr.Guards.BeforeReportPhase(
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
	if err := workeradmission.ObserveCoordinatorTaskInFlight(context.Background(), deps, sess, "task", map[string]any{
		"agent_type": "security-reviewer",
	}, gc); err != nil {
		t.Fatalf("workeradmission.ObserveCoordinatorTaskInFlight: %v", err)
	}
	if !gc.PhaseObligationPending || gc.PhaseObligationKinds != "scan" {
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
			return workeradmission.ObserveCoordinatorTaskInFlight(context.Background(), deps, sess, "task", nil, gc)
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
