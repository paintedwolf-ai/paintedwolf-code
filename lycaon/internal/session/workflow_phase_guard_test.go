package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/oar"
	workflowfacts "github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
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
	workflowFixture1 := phaseGuardWorkflowView{state: workflowfacts.WorkflowPhaseGuardState{
		Phase:                 "ingest",
		ReportCloseoutPending: true,
	}}
	mgr.workflows = &WorkflowDomains{Runs: workflowFixture1, Policy: workflowFixture1, Ambient: workflowFixture1, Blueprints: workflowFixture1, Batch: workflowFixture1, Slash: workflowFixture1, Requests: workflowFixture1, Feedback: workflowFixture1, Transcript: workflowFixture1, Asks: workflowFixture1, Fanout: workflowFixture1, Phases: workflowFixture1, Reports: workflowFixture1, Recovery: workflowFixture1, Cleanup: workflowFixture1}

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
	workflowFixture2 := phaseGuardWorkflowView{state: workflowfacts.WorkflowPhaseGuardState{
		Phase:                 "ingest",
		ReportCloseoutPending: true,
	}}
	mgr.workflows = &WorkflowDomains{Runs: workflowFixture2, Policy: workflowFixture2, Ambient: workflowFixture2, Blueprints: workflowFixture2, Batch: workflowFixture2, Slash: workflowFixture2, Requests: workflowFixture2, Feedback: workflowFixture2, Transcript: workflowFixture2, Asks: workflowFixture2, Fanout: workflowFixture2, Phases: workflowFixture2, Reports: workflowFixture2, Recovery: workflowFixture2, Cleanup: workflowFixture2}
	if _, blocked := mgr.maybeRejectCloseoutBeforeReportPhase(
		context.Background(), sess, "Still collecting scans.", "implement_synthesis", true,
	); blocked {
		t.Fatal("interim prose must not be treated as a completion report")
	}

	workflowFixture3 := phaseGuardWorkflowView{state: workflowfacts.WorkflowPhaseGuardState{Phase: "report"}}

	mgr.workflows = &WorkflowDomains{Runs: workflowFixture3, Policy: workflowFixture3, Ambient: workflowFixture3, Blueprints: workflowFixture3, Batch: workflowFixture3, Slash: workflowFixture3, Requests: workflowFixture3, Feedback: workflowFixture3, Transcript: workflowFixture3, Asks: workflowFixture3, Fanout: workflowFixture3, Phases: workflowFixture3, Reports: workflowFixture3, Recovery: workflowFixture3, Cleanup: workflowFixture3}
	if _, blocked := mgr.maybeRejectCloseoutBeforeReportPhase(
		context.Background(), sess, `{"synthesis":"done"}`, "implement_synthesis", true,
	); blocked {
		t.Fatal("report phase must accept the completion report")
	}
}

func TestTaskObservationBlocksPendingPhaseObligation(t *testing.T) {
	sess := &api.Session{ID: "session-1", AgentType: "coordinator"}
	gc := &oar.GuardContext{}
	deps := WorkerCycleGuardDeps{PhaseGuardState: func(context.Context, string) workflowfacts.WorkflowPhaseGuardState {
		return workflowfacts.WorkflowPhaseGuardState{
			Phase: "ingest", PhaseObligationPending: true, PendingObligationKinds: []string{"scan"},
		}
	}}
	if err := ObserveCoordinatorTaskInFlight(context.Background(), deps, sess, "task", map[string]any{
		"agent_type": "security-reviewer",
	}, gc); err != nil {
		t.Fatalf("ObserveCoordinatorTaskInFlight: %v", err)
	}
	if !gc.PhaseObligationPending || gc.PhaseObligationKinds != "scan" {
		t.Fatal("phase obligation facts were not published")
	}
	if _, ok := gc.RejectData[workflowObligationPendingCode]; !ok {
		t.Fatalf("reject data = %#v", gc.RejectData)
	}
}

func TestTaskPolicyRejectsPendingPhaseObligation(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	deps := WorkerCycleGuardDeps{PhaseGuardState: func(context.Context, string) workflowfacts.WorkflowPhaseGuardState {
		return workflowfacts.WorkflowPhaseGuardState{
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
