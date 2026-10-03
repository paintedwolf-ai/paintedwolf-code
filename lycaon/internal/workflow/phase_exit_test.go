package workflow

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// Prompt tests cover each projected exit kind.

func TestProjectPhaseExit_PlanPhases(t *testing.T) {
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	m, err := reg.Get("plan", "1.0.0")
	testutil.FailErr(t, "Get plan", err)
	cases := []struct {
		phase string
		kind  string
		auth  string
	}{
		{"research", PhaseExitKindProof, string(workflowdef.AdvanceWhenGateMetCoordinator)},
		{"expand", PhaseExitKindProof, string(workflowdef.AdvanceWhenGateMetAuto)},
		{"approve", PhaseExitKindHumanApproval, string(workflowdef.AdvanceWhenGateMetAuto)},
		{"review", PhaseExitKindReviewLoop, string(workflowdef.AdvanceWhenGateMetAuto)},
		{"execute", PhaseExitKindInvoke, string(workflowdef.AdvanceWhenGateMetAuto)},
		{"done", PhaseExitKindTerminal, string(workflowdef.AdvanceWhenGateMetAuto)},
	}
	for _, tc := range cases {
		def, ok := m.PhaseByID(tc.phase)
		if !ok {
			t.Fatalf("missing phase %q", tc.phase)
		}
		got := ProjectPhaseExit(m, def, nil, nil)
		if got.Kind != tc.kind {
			t.Fatalf("%s kind = %q want %q", tc.phase, got.Kind, tc.kind)
		}
		if got.AdvanceAuthority != tc.auth {
			t.Fatalf("%s auth = %q want %q", tc.phase, got.AdvanceAuthority, tc.auth)
		}
		wantCoordinator := tc.auth == string(workflowdef.AdvanceWhenGateMetCoordinator)
		if got.CoordinatorAdvances != wantCoordinator {
			t.Fatalf("%s CoordinatorAdvances = %v want %v", tc.phase, got.CoordinatorAdvances, wantCoordinator)
		}
	}
	approve, _ := m.PhaseByID("approve")
	approveExit := ProjectPhaseExit(m, approve, nil, nil)
	if len(approveExit.ChoiceTransitions) != 1 || approveExit.ChoiceTransitions[0].ID != "critique" {
		t.Fatalf("approve choice arms = %+v want critique", approveExit.ChoiceTransitions)
	}
	if !approveExit.HumanApproval {
		t.Fatal("approve should set HumanApproval")
	}
	research, _ := m.PhaseByID("research")
	researchExit := ProjectPhaseExit(m, research, nil, nil)
	if !slices.Contains(researchExit.OpenGates, "research_satisfied") {
		t.Fatalf("research should report its gate open: %v", researchExit.OpenGates)
	}
}

func TestProjectPhaseExit_ProofGateStates(t *testing.T) {
	m := workflowdef.Manifest{}
	phase := workflowdef.PhaseDef{
		ID:                 "work",
		CompleteWhen:       workflowdef.CompleteWhenGatesSatisfied,
		Gates:              []string{"worker_cycle_ready"},
		AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetAuto,
	}

	satisfied := ProjectPhaseExit(m, phase, []PhaseGateSnapshot{{ID: "worker_cycle_ready", Satisfied: true}}, nil)
	if len(satisfied.OpenGates) != 0 || len(satisfied.DormantGates) != 0 {
		t.Fatalf("satisfied gate should leave nothing open or dormant: %+v", satisfied)
	}
	if satisfied.CoordinatorAdvances {
		t.Fatal("auto-advance phase must not hand the advance to the coordinator")
	}

	// Dormant is not satisfied: an event-scoped gate that never activated does
	// not project as a phase with nothing left to do.
	dormant := ProjectPhaseExit(m, phase, []PhaseGateSnapshot{{ID: "worker_cycle_ready", Dormant: true}}, nil)
	if !slices.Contains(dormant.DormantGates, "worker_cycle_ready") {
		t.Fatalf("dormant gate should be reported dormant: %+v", dormant)
	}
	if len(dormant.OpenGates) != 0 {
		t.Fatalf("dormant gate must not also be open: %+v", dormant)
	}

	open := ProjectPhaseExit(m, phase, []PhaseGateSnapshot{{ID: "worker_cycle_ready"}}, nil)
	if !slices.Contains(open.OpenGates, "worker_cycle_ready") {
		t.Fatalf("open gate should be named: %+v", open)
	}

	multi := ProjectPhaseExit(m, phase, []PhaseGateSnapshot{{ID: "a"}, {ID: "b"}}, nil)
	if strings.Join(multi.OpenGates, ",") != "a,b" {
		t.Fatalf("multiple open gates should be listed: %v", multi.OpenGates)
	}

	coordinatorPhase := phase
	coordinatorPhase.AdvanceWhenGateMet = workflowdef.AdvanceWhenGateMetCoordinator
	call := ProjectPhaseExit(m, coordinatorPhase, []PhaseGateSnapshot{{ID: "worker_cycle_ready", Satisfied: true}}, nil)
	if !call.CoordinatorAdvances {
		t.Fatalf("coordinator-advance phase should report the advance as the coordinator's: %+v", call)
	}
}

func TestProjectPhaseExit_GatelessProofNamesCompleteWhen(t *testing.T) {
	m := workflowdef.Manifest{}
	phase := workflowdef.PhaseDef{
		ID:                 "done-soon",
		CompleteWhen:       "orchestration_complete",
		AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetAuto,
	}
	exit := ProjectPhaseExit(m, phase, nil, nil)
	if exit.CompleteWhen != "orchestration_complete" {
		t.Fatalf("gateless proof phase should carry its completion condition: %q", exit.CompleteWhen)
	}
}

func TestProjectPhaseExitReviewLoopNamesOwedReviewers(t *testing.T) {
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	m, err := reg.Get("security-survey", "1.0.1")
	testutil.FailErr(t, "Get security-survey", err)
	challenge, ok := m.PhaseByID("challenge")
	if !ok {
		t.Fatal("missing phase challenge")
	}
	exit := ProjectPhaseExit(m, challenge, nil, []string{"skeptic", "web-researcher"})
	if got := strings.Join(exit.ReviewAgents, ","); got != "skeptic,web-researcher" {
		t.Fatalf("review agents = %q", got)
	}
	if exit.ReviewLoopKey != "survey_challenged" {
		t.Fatalf("review loop key = %q", exit.ReviewLoopKey)
	}
	if exit.VerdictSchema["verdict"] != "CHALLENGED|NEEDS_INVESTIGATION" {
		t.Fatalf("verdict = %q want CHALLENGED|NEEDS_INVESTIGATION", exit.VerdictSchema["verdict"])
	}
}

func TestProjectPhaseExitReviewLoopFallsBackToDeclaredRoster(t *testing.T) {
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	m, err := reg.Get("security-survey", "1.0.1")
	testutil.FailErr(t, "Get security-survey", err)
	challenge, ok := m.PhaseByID("challenge")
	if !ok {
		t.Fatal("missing phase challenge")
	}
	exit := ProjectPhaseExit(m, challenge, nil, nil)
	if !slices.Contains(exit.ReviewAgents, "skeptic") {
		t.Fatalf("declared-roster fallback must name required agents: %v", exit.ReviewAgents)
	}
}
