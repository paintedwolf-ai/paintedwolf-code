package workflow

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestSatisfyGateInVarsHumanApproval(t *testing.T) {
	vars := SatisfyGateInVars(nil, "human_approval")
	if !conditions.DotPathTruthy(vars, "human_approval.issued") {
		t.Fatal("expected human_approval.issued")
	}
}

func TestStampPhaseGatesOnLeave(t *testing.T) {
	vars := stampPhaseGatesOnLeave(nil, workflowdef.PhaseDef{
		ID:    "research",
		Gates: []string{"research_satisfied"},
	})
	gates, _ := vars["gates"].(map[string]any)
	if ok, _ := gates["research_satisfied"].(bool); !ok {
		t.Fatalf("expected research_satisfied stamped; vars=%v", vars)
	}

	vars = stampPhaseGatesOnLeave(map[string]any{"other": true}, workflowdef.PhaseDef{
		ID:    "expand",
		Gates: []string{"plan_stub_valid", ""},
	})
	gates, _ = vars["gates"].(map[string]any)
	if ok, _ := gates["plan_stub_valid"].(bool); !ok {
		t.Fatalf("expected plan_stub_valid stamped; vars=%v", vars)
	}
	if _, kept := vars["other"]; !kept {
		t.Fatal("expected other key preserved")
	}
}
