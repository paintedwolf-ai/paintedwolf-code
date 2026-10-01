package oar

func decisionFromRule(r *Rule, gc *GuardContext, data map[string]any) Decision {
	if data == nil {
		data = rejectDataFor(gc, r.ID)
	}
	return Decision{
		Effect: r.Effect,
		Code:   r.ID,
		Rule:   r.Qualified(),
		Data:   data,
		OnFire: append([]OnFireAction(nil), r.OnFire...),
		Copy:   RenderRuleCopy(r, activation(gc)),
	}
}
