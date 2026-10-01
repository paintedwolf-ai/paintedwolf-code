package search

import (
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

func compileCodePaths(query Node, flags MatchFlags) (pathGlobFilter, error) {
	paths, err := compilePathGlobs(flags)
	if err != nil {
		return pathGlobFilter{}, err
	}
	paths.hiddenTargets = positivePathTargets(query, false)
	return paths, nil
}

func positivePathTargets(query Node, negated bool) []string {
	switch node := query.(type) {
	case FilterExpr:
		if node.Field == "path" && !negated {
			return []string{node.Value}
		}
	case NotExpr:
		return positivePathTargets(node.Expr, !negated)
	case AndExpr:
		var out []string
		for _, child := range node.Exprs {
			out = append(out, positivePathTargets(child, negated)...)
		}
		return out
	case OrExpr:
		var out []string
		for _, child := range node.Exprs {
			out = append(out, positivePathTargets(child, negated)...)
		}
		return out
	}
	return nil
}

func (f pathGlobFilter) codeScope() sourcecatalog.FileScope {
	if len(f.hiddenTargets) > 0 || len(f.include) > 0 {
		return sourcecatalog.FileScope{Audience: sourcecatalog.HumanAudience, IncludeHidden: true}
	}
	return sourcecatalog.FileScope{Audience: sourcecatalog.HumanAudience}
}

// Explicit path filters and include globs admit matching hidden files only.
func (f pathGlobFilter) allowsCode(rel string) bool {
	if !f.allows(rel) {
		return false
	}
	hidden := false
	for _, segment := range strings.Split(rel, "/") {
		hidden = hidden || sandbox.IsHiddenName(segment)
	}
	if !hidden || len(f.include) > 0 {
		return true
	}
	for _, target := range f.hiddenTargets {
		if pathFilterMatch(target, rel) {
			return true
		}
	}
	return false
}
