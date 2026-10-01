package inject

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildActiveWorkflowInjectData_RuntimeFields(t *testing.T) {
	runCtx := api.CoordinatorRunContext{
		WorkflowID:    "default-pipeline",
		RunID:         "run-1",
		RunStatus:     "running",
		CurrentPhase:  "research",
		FailedLeaves:  []string{"topology_stage_complete"},
		AllowedAgents: []string{"coordinator"},
	}
	snap := WorkflowRuntimeSnapshot{
		Topology: "default-pipeline",
		Phases: []WorkflowPhaseRow{
			{ID: "research", CompleteWhen: "topology_stage_complete", BindTopologyStage: "research",
				Gates: []WorkflowGateState{{ID: "topology_stage_complete", Satisfied: false}}},
			{ID: "plan", CompleteWhen: "topology_stage_complete", BindTopologyStage: "plan"},
		},
	}
	data := BuildActiveWorkflowInjectData(CoordinatorTurnFrame{RunContext: runCtx, Runtime: snap})
	if data.CompleteWhen != "topology_stage_complete" {
		t.Fatalf("complete_when = %q", data.CompleteWhen)
	}
	if data.TopologyPhaseID != "research" {
		t.Fatalf("topology_phase_id = %q", data.TopologyPhaseID)
	}
	if len(data.Phases) != 2 || !data.Phases[0].IsCurrent {
		t.Fatalf("phases = %+v", data.Phases)
	}
	if len(data.FailedLeaves) != 1 || data.FailedLeaves[0] != "topology_stage_complete" {
		t.Fatalf("failed_leaves = %+v", data.FailedLeaves)
	}
	if len(data.UnsatisfiedGateLeaves) != 1 || data.UnsatisfiedGateLeaves[0] != "topology_stage_complete" {
		t.Fatalf("unsatisfied_gate_leaves = %+v", data.UnsatisfiedGateLeaves)
	}
}

func TestBuildActiveWorkflowInjectData_InheritedBlueprintApproval(t *testing.T) {
	approval := &BlueprintApprovalView{Status: "approved", Origin: "inherited", ParentRunID: "parent-1"}
	data := BuildActiveWorkflowInjectData(CoordinatorTurnFrame{
		Runtime: WorkflowRuntimeSnapshot{BlueprintApproval: approval},
	})
	if data.BlueprintApproval == nil || *data.BlueprintApproval != *approval {
		t.Fatalf("BlueprintApproval = %+v want %+v", data.BlueprintApproval, approval)
	}
	approval.Status = "invalid"
	if data.BlueprintApproval.Status != "approved" {
		t.Fatal("inject data must own a snapshot of Blueprint approval")
	}
}

func TestBuildActiveWorkflowInjectData_FlowBrief(t *testing.T) {
	runCtx := api.CoordinatorRunContext{WorkflowID: "plan", CurrentPhase: "review"}
	snap := WorkflowRuntimeSnapshot{
		Phases: []WorkflowPhaseRow{
			{ID: "research", CompleteWhen: "gates_satisfied", Next: "review"},
			{ID: "review", CompleteWhen: "gates_satisfied", Next: "approve",
				Gates: []WorkflowGateState{{ID: "evidence_passed:plan_review", Satisfied: false}}},
			{ID: "approve", Terminal: true},
		},
	}
	data := BuildActiveWorkflowInjectData(CoordinatorTurnFrame{RunContext: runCtx, Runtime: snap})
	if data.PhaseIndex != 2 || data.PhaseTotal != 3 {
		t.Fatalf("position = %d/%d", data.PhaseIndex, data.PhaseTotal)
	}
	if data.NextPhase != "approve" {
		t.Fatalf("next = %q", data.NextPhase)
	}
	if len(data.CurrentGates) != 1 || data.CurrentGates[0].Satisfied {
		t.Fatalf("current gates = %+v", data.CurrentGates)
	}
	if len(data.UnsatisfiedGateLeaves) != 1 || data.UnsatisfiedGateLeaves[0] != "evidence_passed:plan_review" {
		t.Fatalf("unsatisfied = %+v", data.UnsatisfiedGateLeaves)
	}
	if !data.Phases[2].Terminal {
		t.Fatalf("phase flags = %+v", data.Phases)
	}
}

func TestBuildActiveWorkflowInjectData_OmitsDormantEventGate(t *testing.T) {
	runCtx := api.CoordinatorRunContext{
		CurrentPhase: "work",
		FailedLeaves: []string{"worker_cycle_ready"},
	}
	snap := WorkflowRuntimeSnapshot{Phases: []WorkflowPhaseRow{{
		ID: "work",
		Gates: []WorkflowGateState{{
			ID:      "worker_cycle_ready",
			Dormant: true,
		}},
	}}}

	data := BuildActiveWorkflowInjectData(CoordinatorTurnFrame{RunContext: runCtx, Runtime: snap})
	if len(data.CurrentGates) != 0 || len(data.UnsatisfiedGateLeaves) != 0 {
		t.Fatalf("dormant gate projected as obligation: %+v", data.CurrentGates)
	}
	if len(data.FailedLeaves) != 0 {
		t.Fatalf("dormant gate retained stale failed leaves: %v", data.FailedLeaves)
	}
}

func TestBuildActiveWorkflowInjectData_KeepsFailedLeavesWithoutGates(t *testing.T) {
	runCtx := api.CoordinatorRunContext{
		CurrentPhase: "hunt",
		FailedLeaves: []string{"parallel_stages_complete"},
	}
	snap := WorkflowRuntimeSnapshot{Phases: []WorkflowPhaseRow{{
		ID:           "hunt",
		CompleteWhen: "parallel_stages_complete",
	}}}

	data := BuildActiveWorkflowInjectData(CoordinatorTurnFrame{RunContext: runCtx, Runtime: snap})
	if len(data.FailedLeaves) != 1 || data.FailedLeaves[0] != "parallel_stages_complete" {
		t.Fatalf("failed_leaves = %+v", data.FailedLeaves)
	}
	if data.CompleteWhen != "parallel_stages_complete" {
		t.Fatalf("complete_when = %q", data.CompleteWhen)
	}
}

func TestAttachGateObligationsProjectsUnsatisfiedLeaves(t *testing.T) {
	catalog, err := feedback.LoadGateFeedbackCatalog()
	if err != nil {
		t.Fatalf("LoadGateFeedbackCatalog: %v", err)
	}
	data := BuildActiveWorkflowInjectData(CoordinatorTurnFrame{
		RunContext: api.CoordinatorRunContext{WorkflowID: "wf", CurrentPhase: "research", AdvanceWhenGateMet: "coordinator"},
		Runtime: WorkflowRuntimeSnapshot{Phases: []WorkflowPhaseRow{{
			ID:    "research",
			Gates: []WorkflowGateState{{ID: "research_satisfied", Satisfied: false}},
		}}},
	})
	data = AttachGateObligations(t.Context(), data, catalog, "coordinator", "")
	if len(data.GateObligations) != 1 || data.GateObligations[0].ID != "research_satisfied" {
		t.Fatalf("obligations = %+v", data.GateObligations)
	}
	joined := strings.Join(data.GateObligations[0].Satisfy, "\n")
	if !strings.Contains(joined, "research") {
		t.Fatalf("satisfy missing research guidance: %q", joined)
	}

	data.CurrentGates[0].Satisfied = true
	data.UnsatisfiedGateLeaves = UnsatisfiedGateIDs(data.CurrentGates)
	data.GateObligations = nil
	data = AttachGateObligations(t.Context(), data, catalog, "coordinator", "")
	if len(data.GateObligations) != 0 {
		t.Fatalf("satisfied gate must clear obligations; got %+v", data.GateObligations)
	}
}

func TestBuildActiveWorkflowInjectData_NextPhaseFallsBackToSequence(t *testing.T) {
	runCtx := api.CoordinatorRunContext{CurrentPhase: "research"}
	snap := WorkflowRuntimeSnapshot{Phases: []WorkflowPhaseRow{
		{ID: "research"},
		{ID: "expand"},
	}}
	data := BuildActiveWorkflowInjectData(CoordinatorTurnFrame{RunContext: runCtx, Runtime: snap})
	if data.NextPhase != "expand" {
		t.Fatalf("next = %q (want implicit sequence successor)", data.NextPhase)
	}
}

func TestActiveWorkflowInjectFingerprint_GateFlip(t *testing.T) {
	runCtx := api.CoordinatorRunContext{CurrentPhase: "review"}
	snap := WorkflowRuntimeSnapshot{Phases: []WorkflowPhaseRow{
		{ID: "review", Gates: []WorkflowGateState{{ID: "evidence_passed:plan_review", Satisfied: false}}},
	}}
	a := ActiveWorkflowInjectFingerprint(BuildActiveWorkflowInjectData(CoordinatorTurnFrame{RunContext: runCtx, Runtime: snap}), nil)
	snap.Phases[0].Gates[0].Satisfied = true
	b := ActiveWorkflowInjectFingerprint(BuildActiveWorkflowInjectData(CoordinatorTurnFrame{RunContext: runCtx, Runtime: snap}), nil)
	if a == b {
		t.Fatal("gate satisfaction flip should alter fingerprint")
	}
}

func TestActiveWorkflowInjectFingerprint_PhaseListChange(t *testing.T) {
	base := BuildActiveWorkflowInjectData(CoordinatorTurnFrame{
		RunContext: api.CoordinatorRunContext{WorkflowID: "wf"},
		Runtime: WorkflowRuntimeSnapshot{
			Phases: []WorkflowPhaseRow{{ID: "research", CompleteWhen: "topology_stage_complete"}},
		},
	})
	a := ActiveWorkflowInjectFingerprint(base, nil)
	changed := base
	changed.Phases = append(changed.Phases, ActiveWorkflowPhaseView{ID: "plan"})
	b := ActiveWorkflowInjectFingerprint(changed, nil)
	if a == b {
		t.Fatal("phase list change should alter fingerprint")
	}
}

func TestActiveWorkflowInjectFingerprint_RenderedStateChanges(t *testing.T) {
	base := ActiveWorkflowInjectData{
		FailedLeaves:          []string{"failed"},
		UnsatisfiedGateLeaves: []string{"open"},
		GateObligations: []GateObligationView{{
			ID: "open", Purpose: "purpose", Satisfy: []string{"step"},
			Missing: []string{"missing"}, Required: []string{"required"},
		}},
		PhaseExit: &PhaseExitView{ReviewLoopCap: 1},
	}
	wantDifferent := []ActiveWorkflowInjectData{
		{
			FailedLeaves:          []string{"changed"},
			UnsatisfiedGateLeaves: base.UnsatisfiedGateLeaves,
			GateObligations:       base.GateObligations,
			PhaseExit:             base.PhaseExit,
		},
		{
			FailedLeaves:          base.FailedLeaves,
			UnsatisfiedGateLeaves: []string{"changed"},
			GateObligations:       base.GateObligations,
			PhaseExit:             base.PhaseExit,
		},
		{
			FailedLeaves:          base.FailedLeaves,
			UnsatisfiedGateLeaves: base.UnsatisfiedGateLeaves,
			GateObligations: []GateObligationView{{
				ID: "open", Purpose: "purpose", Satisfy: []string{"step"},
				Missing: []string{"changed"}, Required: []string{"required"},
			}},
			PhaseExit: base.PhaseExit,
		},
		{
			FailedLeaves:          base.FailedLeaves,
			UnsatisfiedGateLeaves: base.UnsatisfiedGateLeaves,
			GateObligations:       base.GateObligations,
			PhaseExit:             &PhaseExitView{ReviewLoopCap: 2},
		},
	}
	want := ActiveWorkflowInjectFingerprint(base, nil)
	for i, changed := range wantDifferent {
		if got := ActiveWorkflowInjectFingerprint(changed, nil); got == want {
			t.Fatalf("case %d retained fingerprint", i)
		}
	}
}

func TestActiveWorkflowFingerprintIncludesReportContract(t *testing.T) {
	data := ActiveWorkflowInjectData{WorkflowID: "review", RunID: "run"}
	before := ActiveWorkflowInjectFingerprint(data, nil)
	data.ReportDocumentEnabled = true
	if before == ActiveWorkflowInjectFingerprint(data, nil) {
		t.Fatal("changed report contract must replace the injected instructions")
	}
}
