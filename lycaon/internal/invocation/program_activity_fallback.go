package invocation

func assignActivityFallbacks(groups []activityGroup, byKey map[activityKey]int, primary map[int]bool, assignments map[int]int) {
	hasAssignedDescendant := map[activityKey]bool{}
	for i := range assignments {
		markAssignedAncestors(groups[i], groups, byKey, hasAssignedDescendant)
	}

	// Parents record first, so fallback descendants claim their rows before containers.
	for i := len(groups) - 1; i >= 0; i-- {
		g := groups[i]
		if _, assigned := assignments[i]; assigned {
			continue
		}
		c := g.counts
		// Outcome normalization must preserve a row shown before settlement.
		if c.failed+c.refused+c.interrupted+c.waiting+c.closed+c.recovered == 0 {
			continue
		}
		if activityPresentation(g.input).role == "container" && hasAssignedDescendant[g.key] {
			continue
		}
		primary[i] = true
		assignments[i] = i
		markAssignedAncestors(g, groups, byKey, hasAssignedDescendant)
	}
}

func markAssignedAncestors(g activityGroup, groups []activityGroup, byKey map[activityKey]int, marked map[activityKey]bool) {
	for key := g.parent; key != (activityKey{}) && key != g.key && !marked[key]; {
		marked[key] = true
		i, ok := byKey[key]
		if !ok {
			break
		}
		key = groups[i].parent
	}
}
