package summarize

// Rolled-up children keep their coverage cost; selected children share the
// released depth budget in proportion to their original allocations.
func redistributeRolledUpDepthBudget(plans []childPlan, caps Caps, remaining int) {
	var released, weight, coverage int
	for _, plan := range plans {
		if plan.drill {
			weight += plan.bi
			continue
		}
		cost := caps.EstimateTokens(rollupSkeletonLine(rollupRow(plan.child, nil)))
		coverage += cost
		released += max(0, plan.bi-cost)
	}
	// Minimum-drill rescue may already have spent part of this pool.
	released = min(released, max(0, remaining-coverage-weight))
	if released == 0 || weight == 0 {
		return
	}
	for i := range plans {
		if !plans[i].drill {
			continue
		}
		original := plans[i].bi
		share := released * original / weight
		plans[i].bi += share
		released -= share
		weight -= original
	}
}
