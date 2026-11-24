package workflow

import (
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func checkAskFanoutPlan(plan runstate.FanoutPlan) error {
	for _, leg := range plan.Legs {
		var scope api.TaskScope
		if leg.Scope != nil {
			scope = *leg.Scope
		}
		writes, known := prompts.AgentMutationCapable(leg.AgentType)
		if err := workeradmission.RequireAskReadScope("fanout_plan", writes, known, scope); err != nil {
			return err
		}
	}
	return nil
}
