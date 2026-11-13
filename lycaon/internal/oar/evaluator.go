package oar

import (
	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/oarcore"
)

// selectorMatches applies declared string and list membership clauses.
func selectorMatches(sel Selector, values map[string]any) bool {
	if len(sel) == 0 {
		return true
	}
	for _, clause := range sel.Clauses() {
		want := sel[clause]
		if len(want) == 0 {
			// An empty clause matches nothing.
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

// containsExact compares by exact code-point equality.
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
		return r.document.MatchSelector(s.values, selectorDimensions(s.context, s.values))
	}
	return selectorMatches(r.Selector, s.values), nil
}

// selectorDimensions are the selector dimensions this host models beyond the
// core facts ([OAR-SEL-6]). An unbound phase or workflow is nil.
func selectorDimensions(gc *GuardContext, values map[string]any) oarcore.Dimensions {
	dims := oarcore.Dimensions{"phase": nil, "workflow": nil}
	if gc == nil {
		return dims
	}
	switch {
	case gc.Session.WorkerLeg:
		dims["surface"] = "worker"
	case gc.Session.Phase != "":
		dims["surface"] = "phase"
	default:
		dims["surface"] = "coordinator"
	}
	if gc.Session.Surface != "" {
		dims["surfaces"] = gc.Session.Surface
	}
	if gc.Session.Phase != "" {
		dims["phase"] = gc.Session.Phase
	}
	if id, _ := values[oarcopy.HostFactNamespace+".workflow_id"].(string); id != "" {
		dims["workflow"] = id
	}
	var principal []string
	if gc.Session.Principal != "" {
		principal = append(principal, gc.Session.Principal)
	}
	dims["principal"] = append(principal, gc.Session.PrincipalRoles...)
	return dims
}
