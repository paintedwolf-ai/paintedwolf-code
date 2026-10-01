package search

// StorePostFilter applies match flags and path globs to SQL candidates.
type StorePostFilter struct {
	// expr mirrors the query AST with text matchers and SQL probes.
	expr storePostExpr
	// numProbes is the count of probe columns appended to the SELECT.
	numProbes int
	paths     pathGlobFilter
}

// keep applies globs only to rows that have paths.
func (f *StorePostFilter) keep(path string, texts []string, probes []bool) bool {
	if path != "" && !f.paths.allows(path) {
		return false
	}
	if f.expr == nil {
		return true
	}
	return f.expr.eval(texts, probes)
}

// storePostExpr evaluates one query node against candidate text and SQL probes.
type storePostExpr interface {
	eval(texts []string, probes []bool) bool
}

type postAndExpr struct{ children []storePostExpr }

func (e postAndExpr) eval(texts []string, probes []bool) bool {
	for _, child := range e.children {
		if !child.eval(texts, probes) {
			return false
		}
	}
	return true
}

type postOrExpr struct{ children []storePostExpr }

func (e postOrExpr) eval(texts []string, probes []bool) bool {
	for _, child := range e.children {
		if child.eval(texts, probes) {
			return true
		}
	}
	return false
}

type postNotExpr struct{ child storePostExpr }

func (e postNotExpr) eval(texts []string, probes []bool) bool {
	return !e.child.eval(texts, probes)
}

// postProbeExpr reads one structured filter's SQL result.
type postProbeExpr struct{ index int }

func (e postProbeExpr) eval(_ []string, probes []bool) bool {
	return e.index < len(probes) && probes[e.index]
}

// postTrueExpr represents nodes fully enforced by SQL.
type postTrueExpr struct{}

func (postTrueExpr) eval([]string, []bool) bool { return true }

// postTextExpr applies one flagged free-text term to every candidate text.
type postTextExpr struct{ matcher textMatcher }

func (e postTextExpr) eval(texts []string, _ []bool) bool {
	for _, text := range texts {
		if text != "" && e.matcher.matches(text) {
			return true
		}
	}
	return false
}
