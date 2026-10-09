package search

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"

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
	if f.dependencyHidden != nil || len(f.hiddenTargets) > 0 || len(f.include) > 0 {
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
	return f.dependencyHidden != nil && f.dependencyHidden(rel)
}

// Dependency requests admit hidden artifacts only inside a declared lazy boundary.
func (f pathGlobFilter) withDependencies(ctx context.Context, catalog *sourcecatalog.Catalog, root string, include bool) pathGlobFilter {
	if !include {
		return f
	}
	var mu sync.Mutex
	cached := make(map[string]bool)
	f.dependencyHidden = func(rel string) bool {
		parent := path.Dir(rel)
		mu.Lock()
		defer mu.Unlock()
		if allowed, ok := cached[parent]; ok {
			return allowed
		}
		allowed := catalog.BoundaryPath(ctx, root, parent, true) != ""
		if len(cached) >= 1024 {
			clear(cached)
		}
		cached[parent] = allowed
		return allowed
	}
	return f
}

type pathGlobFilter struct {
	include          []sandbox.EntryGlob
	exclude          []sandbox.EntryGlob
	hiddenTargets    []string
	dependencyHidden func(string) bool
}

func compilePathGlobs(flags MatchFlags) (pathGlobFilter, error) {
	include, err := compilePathGlobList("include", flags.Include)
	if err != nil {
		return pathGlobFilter{}, err
	}
	exclude, err := compilePathGlobList("exclude", flags.Exclude)
	if err != nil {
		return pathGlobFilter{}, err
	}
	return pathGlobFilter{include: include, exclude: exclude}, nil
}

func compilePathGlobList(field string, patterns []string) ([]sandbox.EntryGlob, error) {
	var compiled []sandbox.EntryGlob
	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) == "" {
			continue
		}
		glob, err := sandbox.CompileEntryGlob(pattern)
		if err != nil {
			return nil, &MatchError{Message: fmt.Sprintf("invalid %s glob %q: %v", field, pattern, err)}
		}
		compiled = append(compiled, glob)
	}
	return compiled, nil
}

func (f pathGlobFilter) allows(rel string) bool {
	rel = strings.TrimPrefix(strings.TrimSpace(rel), "/")
	for _, glob := range f.exclude {
		if glob.Match(rel) {
			return false
		}
	}
	if len(f.include) == 0 {
		return true
	}
	for _, glob := range f.include {
		if glob.Match(rel) {
			return true
		}
	}
	return false
}
