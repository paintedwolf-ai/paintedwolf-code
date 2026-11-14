package oar

import "slices"

// selectorMatches applies the tools clause: the occurrence's tool is one of
// the listed names, and no tool never matches ([OAR-SEL-6]).
func selectorMatches(sel Selector, values map[string]any) bool {
	want, ok := sel["tools"]
	if !ok {
		return true
	}
	tool, _ := values["tool"].(string)
	return tool != "" && slices.Contains(want, tool)
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
