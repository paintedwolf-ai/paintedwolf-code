package session

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

type phaseGuardWorkflowView struct {
	stubWorkflowManifest
	state WorkflowPhaseGuardState
}

func (v phaseGuardWorkflowView) ActivePhaseGuardState(context.Context, string) WorkflowPhaseGuardState {
	return v.state
}

func TestCloseoutBlocksCompletionReportBeforeReportPhase(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.workflows = phaseGuardWorkflowView{state: WorkflowPhaseGuardState{
		Phase:                 "ingest",
		ReportCloseoutPending: true,
	}}

	reject, blocked := mgr.maybeRejectCloseoutBeforeReportPhase(
		context.Background(), sess, `{"synthesis":"done"}`, "implement_synthesis", true,
	)
	if !blocked {
		t.Fatal("expected completion report to be blocked before report phase")
	}
	if body := reject.Error(); !strings.Contains(body, workflowReportPhaseRequiredCode) || !strings.Contains(body, "ingest") {
		t.Fatalf("reject = %q", body)
	}
}

func TestCloseoutPhaseGuardAllowsInterimProseAndReportPhase(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.workflows = phaseGuardWorkflowView{state: WorkflowPhaseGuardState{
		Phase:                 "ingest",
		ReportCloseoutPending: true,
	}}
	if _, blocked := mgr.maybeRejectCloseoutBeforeReportPhase(
		context.Background(), sess, "Still collecting scans.", "implement_synthesis", true,
	); blocked {
		t.Fatal("interim prose must not be treated as a completion report")
	}

	mgr.workflows = phaseGuardWorkflowView{state: WorkflowPhaseGuardState{Phase: "report"}}
	if _, blocked := mgr.maybeRejectCloseoutBeforeReportPhase(
		context.Background(), sess, `{"synthesis":"done"}`, "implement_synthesis", true,
	); blocked {
		t.Fatal("report phase must accept the completion report")
	}
}

func TestTaskObservationBlocksPendingPhaseObligation(t *testing.T) {
	sess := &api.Session{ID: "session-1", AgentType: "coordinator"}
	gc := &oar.GuardContext{}
	deps := WorkerCycleGuardDeps{PhaseGuardState: func(context.Context, string) WorkflowPhaseGuardState {
		return WorkflowPhaseGuardState{
			Phase: "ingest", PhaseObligationPending: true, PendingObligationKinds: []string{"scan"},
		}
	}}
	if err := ObserveCoordinatorTaskInFlight(context.Background(), deps, sess, "task", map[string]any{
		"agent_type": "security-reviewer",
	}, gc); err != nil {
		t.Fatalf("ObserveCoordinatorTaskInFlight: %v", err)
	}
	if !gc.Workflow.PhaseObligationPending || gc.Workflow.PhaseObligationKinds != "scan" {
		t.Fatal("phase obligation facts were not published")
	}
	if _, ok := gc.RejectData[workflowObligationPendingCode]; !ok {
		t.Fatalf("reject data = %#v", gc.RejectData)
	}
}

func TestTaskPolicyRejectsPendingPhaseObligation(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	deps := WorkerCycleGuardDeps{PhaseGuardState: func(context.Context, string) WorkflowPhaseGuardState {
		return WorkflowPhaseGuardState{
			Phase: "ingest", PhaseObligationPending: true, PendingObligationKinds: []string{"scan"},
		}
	}}
	reject, blocked, err := mgr.tryOARBlock(
		context.Background(), oar.AnchorCoordinatorPreInvoke, sess, "task",
		map[string]any{"agent_type": "security-reviewer"},
		func(gc *oar.GuardContext) error {
			return ObserveCoordinatorTaskInFlight(context.Background(), deps, sess, "task", nil, gc)
		},
	)
	if err != nil {
		t.Fatalf("tryOARBlock: %v", err)
	}
	if !blocked || reject == nil {
		t.Fatal("expected OAR to reject task while a phase obligation is pending")
	}
	if body := reject.Error(); !strings.Contains(body, workflowObligationPendingCode) || !strings.Contains(body, "host obligations (scan)") {
		t.Fatalf("reject = %q", body)
	}
}
