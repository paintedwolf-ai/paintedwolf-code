package detectionpack

// Matches reports whether the rule's condition holds for the event.
// Always false when !Supported, and always false when r.Source != SourceToolExec.
func (r Rule) Matches(ev Event) bool {
	if !r.Supported || r.Source != SourceToolExec || r.condition == nil {
		return false
	}
	if len(ev.executionGroups) > 0 {
		for _, group := range ev.executionGroups {
			if r.matches(group.lookup) {
				return true
			}
		}
		return false
	}
	return r.matches(ev.lookup)
}

// MatchesEgress is the egress-side counterpart. Always false when !Supported,
// and always false when r.Source != SourceEgressObserved.
func (r Rule) MatchesEgress(ev EgressEvent) bool {
	if !r.Supported || r.Source != SourceEgressObserved || r.condition == nil {
		return false
	}
	return r.matches(ev.lookup)
}

func (r Rule) matches(lookup func(string) (any, bool)) bool {
	return r.condition.eval(r.selections, lookup)
}
