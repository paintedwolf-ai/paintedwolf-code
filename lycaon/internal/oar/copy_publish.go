package oar

import "maps"

func decisionFromRule(r *Rule, gc *GuardContext, data map[string]any) Decision {
	if data == nil {
		data = rejectDataFor(gc, r.ID)
	}
	facts := activation(gc)
	if bindsNextSteps(r) {
		steps := ResolveNextSteps(r, gc)
		facts[NextStepsFact] = steps.Text
		if len(steps.Calls) > 0 {
			data = maps.Clone(data)
			if data == nil {
				data = map[string]any{}
			}
			data[NextCallsDataKey] = steps.Calls
		}
	}
	return Decision{
		Effect: r.Effect,
		Code:   r.ID,
		Rule:   r.Qualified(),
		Data:   data,
		OnFire: append([]OnFireAction(nil), r.OnFire...),
		Copy:   RenderRuleCopy(r, facts),
	}
}
