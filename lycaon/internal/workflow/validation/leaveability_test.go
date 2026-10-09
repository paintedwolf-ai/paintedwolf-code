package validation_test

import (
	"github.com/lycaon/lycaon/internal/progress"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"strings"
	"testing"
)

func TestPhaseImpliesHITLToolsFromGateFacts(t *testing.T) {
	if !workflowvalidation.PhaseImpliesHITLTools(workflowdef.PhaseDef{
		ID:    "intake",
		Gates: []string{"hitl_consulted:intake"},
	}) {
		t.Fatal("hitl_consulted gate should imply HITL tools")
	}
	if !workflowvalidation.PhaseImpliesHITLTools(workflowdef.PhaseDef{
		ID:     "q",
		Intake: []string{"size"},
	}) {
		t.Fatal("intake: should imply HITL tools")
	}
	if workflowvalidation.PhaseImpliesHITLTools(workflowdef.PhaseDef{
		ID:           "done",
		CompleteWhen: "orchestration_complete",
		Terminal:     true,
	}) {
		t.Fatal("terminal stamp phase should not imply HITL tools")
	}
}

func TestRequiredSurfaceToolsFromAdvancePolicy(t *testing.T) {
	m := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "x", Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:                 "work",
			AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetCoordinator,
			CompleteWhen:       workflowdef.CompleteWhenGatesSatisfied,
			Gates:              []string{"human_approval"},
		}},
	})
	p := m.PhaseDefs[0]
	tools := workflowvalidation.RequiredSurfaceToolsForPhase(m, p)
	if !containsStr(tools, workflowvalidation.ToolWorkflowAdvance) {
		t.Fatalf("coordinator advance requires workflow_advance; got %v", tools)
	}
	if containsStr(tools, workflowvalidation.ToolAskUser) {
		t.Fatalf("human_approval alone must not require ask_user; got %v", tools)
	}
}

func TestValidateResolvedSurfaceToolsHITL(t *testing.T) {
	m := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "x", Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:                 "intake",
			AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetCoordinator,
			CompleteWhen:       workflowdef.CompleteWhenGatesSatisfied,
			Gates:              []string{"hitl_consulted:intake"},
		}},
	})
	p := m.PhaseDefs[0]
	diags := workflowvalidation.ValidateResolvedSurfaceTools(m, p, "any_surface", []string{"read", "wait"})
	hasHITL := false
	hasAdvance := false
	for _, d := range diags {
		switch d.Code {
		case string(workflowdiag.MustCode("missing_hitl_tools")):
			hasHITL = true
			if !strings.Contains(d.Message, "ask_user") && !strings.Contains(d.Replacement, "ask_user") {
				t.Fatalf("missing_hitl_tools should name ask_user; got message=%q replacement=%q", d.Message, d.Replacement)
			}
		case string(workflowdiag.MustCode("missing_tool_for_advance_policy")):
			hasAdvance = true
		}
	}
	if !hasHITL {
		t.Fatalf("expected missing_hitl_tools for ask_user; diags=%v", diags)
	}
	if !hasAdvance {
		t.Fatalf("expected missing_tool_for_advance_policy; diags=%v", diags)
	}
	// No surface-name fork: complete tool set passes on an arbitrary surface id.
	diags2 := workflowvalidation.ValidateResolvedSurfaceTools(m, p, "not_await_user", []string{"ask_user", "wait", "workflow_advance"})
	if len(diags2) != 0 {
		t.Fatalf("complete tool set should pass on any surface id: %v", diags2)
	}
}

func TestValidateBlueprintWritePhasesRejectsReadOnlySurface(t *testing.T) {
	m := workflowdef.Manifest{
		ID: "decision", Version: "1.0.0",
		Blueprint: &workflowdef.BlueprintDef{ID: "selection"},
		PhaseDefs: []workflowdef.PhaseDef{{
			ID: "judge", CoordinatorSurface: "review_adjudicate", BlueprintWrite: true,
		}},
	}
	diags := workflowvalidation.ValidateBlueprintWritePhases(m)
	if len(diags) != 1 || diags[0].Code != string(workflowdiag.MustCode("blueprint_writer_surface_read_only")) {
		t.Fatalf("read-only Blueprint writer diagnostics = %#v", diags)
	}

	m.PhaseDefs[0].CoordinatorSurface = "decision_adjudicate"
	if diags = workflowvalidation.ValidateBlueprintWritePhases(m); len(diags) != 0 {
		t.Fatalf("writable Blueprint phase diagnostics = %#v", diags)
	}
}

func TestValidateBlueprintWritePhasesRequiresDeclaredWriter(t *testing.T) {
	m := workflowdef.Manifest{
		ID: "decision", Blueprint: &workflowdef.BlueprintDef{ID: "selection"},
		PhaseDefs: []workflowdef.PhaseDef{{ID: "approve", HumanApproval: &workflowdef.HumanApprovalConfig{}}},
	}
	diags := workflowvalidation.ValidateBlueprintWritePhases(m)
	if len(diags) != 1 || diags[0].Code != string(workflowdiag.MustCode("blueprint_writer_required")) {
		t.Fatalf("missing Blueprint writer diagnostics = %#v", diags)
	}
}

func TestSurfaceMissingUpdateProgressUsesHostSSOT(t *testing.T) {
	if !progress.SurfaceMissingUpdateProgress([]string{"edit", "read"}) {
		t.Fatal("edit is progress-gated; missing update_progress should fail")
	}
	if progress.SurfaceMissingUpdateProgress([]string{"edit", progress.AuthoringToolID}) {
		t.Fatal("update_progress present should pass")
	}
	if progress.SurfaceMissingUpdateProgress([]string{"read", "wait"}) {
		t.Fatal("no gated tools should pass")
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
