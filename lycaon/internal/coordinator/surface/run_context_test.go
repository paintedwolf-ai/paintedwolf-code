package surface

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestIsKnownWorkflowPhaseFromSurfaceBinding(t *testing.T) {
	eligible := false
	if !isKnownWorkflowPhase(api.CoordinatorRunContext{
		WorkflowID:              "plan",
		CurrentPhase:            "research",
		PhaseCoordinatorSurface: "plan_research",
	}) {
		t.Fatal("bound plan phase must be known")
	}
	if isKnownWorkflowPhase(api.CoordinatorRunContext{
		WorkflowID:   "custom",
		CurrentPhase: "work",
	}) {
		t.Fatal("unbound custom phase must not be known")
	}
	eligibleTrue := true
	if !isKnownWorkflowPhase(api.CoordinatorRunContext{
		WorkflowID:                  "implement",
		CurrentPhase:                "work",
		WorkflowInvestigateEligible: &eligibleTrue,
	}) {
		t.Fatal("implement work phase must be known via profile flag")
	}
	if isKnownWorkflowPhase(api.CoordinatorRunContext{
		WorkflowID:                  "plan",
		CurrentPhase:                "research",
		WorkflowInvestigateEligible: &eligible,
	}) {
		t.Fatal("plan phase without surface binding must not be known")
	}
}

func TestStaticWorkflowHintCodesImplementWorkPhaseKnown(t *testing.T) {
	eligible := true
	codes := StaticWorkflowHintCodes(api.CoordinatorRunContext{
		WorkflowID:                  "implement",
		CurrentPhase:                "work",
		WorkflowInvestigateEligible: &eligible,
	}, false)
	for _, code := range codes {
		if code == "WORKFLOW_SESSION_COMPOSE_REQUIRED" {
			t.Fatalf("implement work must not require compose: %v", codes)
		}
	}
}
