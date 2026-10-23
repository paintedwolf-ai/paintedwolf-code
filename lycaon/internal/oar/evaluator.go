package oar

// selectorMatches evaluates every clause against the fact it names. A clause
// naming a string fact matches when the value is a member of the clause's list;
// a clause naming a list<string> fact matches when at least one member of the
// value appears in it ([OAR-SEL-1], [OAR-SEL-2]).
//
// An unknown clause never reaches here: the loader rejects it ([OAR-SEL-3]).
func selectorMatches(sel Selector, values map[string]any) bool {
	if len(sel) == 0 {
		return true
	}
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

// selectorFacts belongs to one selection pass; providers may change its source.
type selectorFacts struct {
	context  *GuardContext
	values   map[string]any
	computed int
}

func (s *selectorFacts) match(r *Rule) (bool, error) {
	if err := AssembleFacts(s.context, r.Selector.Clauses()); err != nil {
		return false, err
	}
	if len(r.Selector) == 0 {
		return true, nil
	}
	computed := 0
	if s.context != nil {
		s.context.lazy.mu.Lock()
		computed = len(s.context.lazy.computed)
		s.context.lazy.mu.Unlock()
	}
	// A completed provider can publish several facts, including after an error.
	if s.values == nil || s.computed != computed {
		s.values = activation(s.context)
		s.computed = computed
	}
	if r.document != nil {
		return r.document.MatchSelector(s.values)
	}
	return selectorMatches(r.Selector, s.values), nil
}
