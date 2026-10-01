package oar

// selectorMatches evaluates every clause against the fact it names. A clause
// naming a string fact matches when the value is a member of the clause's list;
// a clause naming a list<string> fact matches when at least one member of the
// value appears in it ([OAR-SEL-1], [OAR-SEL-2]).
//
// An unknown clause never reaches here: the loader rejects it ([OAR-SEL-3]).
func selectorMatches(sel Selector, gc *GuardContext) bool {
	if len(sel) == 0 {
		return true
	}
	values := activation(gc)
	for _, clause := range sel.Clauses() {
		want := sel[clause]
		if len(want) == 0 {
			// [OAR-SEL-6] An empty clause matches nothing.
			return false
		}
		switch value := values[clause].(type) {
		case string:
			if !containsExact(want, value) {
				return false
			}
		case []string:
			if !intersects(want, value) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func intersects(want, have []string) bool {
	for _, v := range have {
		if containsExact(want, v) {
			return true
		}
	}
	return false
}

// containsExact compares by exact code-point equality ([OAR-EXPR-22]).
func containsExact(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func ruleSelectorMatches(r *Rule, gc *GuardContext) (bool, error) {
	if r.document != nil {
		return r.document.MatchSelector(activation(gc))
	}
	return selectorMatches(r.Selector, gc), nil
}
