package oar

import (
	"maps"

	"github.com/lycaon/lycaon/pkg/api"
)

func decisionFromRule(r *Rule, gc *GuardContext, data map[string]any) Decision {
	if data == nil {
		data = rejectDataFor(gc, r.ID)
	}
	facts := activation(gc)
	var nextStepsErr error
	if bindsNextSteps(r) {
		steps := ResolveNextSteps(r, gc)
		nextStepsErr = steps.Err
		facts[NextStepsFact] = steps.Text
		if len(steps.Calls) > 0 {
			data = maps.Clone(data)
			if data == nil {
				data = map[string]any{}
			}
			data[api.ToolFeedbackNextCalls] = steps.Calls
		}
	}
	return Decision{
		Effect: r.Effect,
		Code:   r.ID,
		Rule:   r.Qualified(),
		Data:   data,
		OnFire: append([]OnFireAction(nil), r.OnFire...),
		Copy:   RenderRuleCopy(r, facts),

		NextStepsErr: nextStepsErr,
	}
}
