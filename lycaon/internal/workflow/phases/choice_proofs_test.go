package phases

import (
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"testing"
)

func TestClearChoiceEntryProofsRequiresFreshFanoutPlan(t *testing.T) {
	vars := runstate.StampFanoutPlan(nil, runstate.FanoutPlan{
		Legs: []runstate.FanoutPlanLeg{{AgentType: "path-explorer", Prompt: "Map the initial scope"}},
	})
	vars = runstate.SetGateSatisfied(vars, "fanout_planned", true)

	cleared := clearChoiceEntryProofs(vars, workflowdef.PhaseDef{
		ID:    "drill_plan",
		Gates: []string{"fanout_planned"},
	})
	gates, _ := cleared["gates"].(map[string]any)
	if planned, _ := gates["fanout_planned"].(bool); planned {
		t.Fatal("choice entry retained fanout_planned satisfaction")
	}
}
