package sourcecatalog

import (
	"context"
	"log/slog"
	"path"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcescope"
)

// walkPolicy applies root budgets and traversal order to the walked directory.
type walkPolicy struct {
	budgets             sandbox.SurveyBudgets
	scope               *sourcescope.Scope
	includeDependencies bool
	selectedPaths       []string
	// base is root-relative; "." denotes the scope root.
	base string
}

// under narrows the policy to a subtree walk that starts at rel.
func (p walkPolicy) under(rel string) walkPolicy {
	if p.base != "" && p.base != "." {
		rel = path.Join(p.base, rel)
	}
	if p.scope != nil && p.scope.BoundaryPath(rel, true) != "" {
		p.includeDependencies = true
	}
	p.base = rel
	return p
}

func (p walkPolicy) options() sandbox.SurveyOptions {
	opts := sandbox.SurveyOptions{IncludeHidden: true, Budgets: p.budgets}
	if p.scope != nil {
		opts.Scope = prefixedScope{base: p.base, scope: p.scope, includeDependencies: p.includeDependencies, selectedPaths: p.selectedPaths}
	}
	return opts
}

// deferDir reports whether the scope orders rel after its siblings.
func (p walkPolicy) deferDir(rel string) bool {
	if p.scope == nil {
		return false
	}
	return prefixedScope{base: p.base, scope: p.scope}.DeferDir(rel, "")
}

// collapseDir reports whether a recursive expansion leaves rel closed.
func (p walkPolicy) collapseDir(rel string) bool {
	if p.scope == nil {
		return false
	}
	return p.boundaryDir(rel) != "" || prefixedScope{base: p.base, scope: p.scope}.CollapseDir(rel, "")
}

// prefixedScope translates subtree paths into root-relative scope paths.
type prefixedScope struct {
	includeDependencies bool
	selectedPaths       []string
	base                string
	scope               *sourcescope.Scope
}

func (s prefixedScope) rel(rel string) string {
	if s.base == "" || s.base == "." {
		return rel
	}
	return path.Join(s.base, rel)
}

func (s prefixedScope) PruneDir(rel, abs string) (string, bool) {
	if !selectedCatalogPath(s.rel(rel), true, s.selectedPaths) {
		return "request_scope", true
	}
	return s.scope.PruneDir(s.rel(rel), abs)
}

func (s prefixedScope) LazyDir(rel, _ string) (string, bool) {
	if s.includeDependencies {
		return "", false
	}
	reason := s.scope.BoundaryDir(s.rel(rel))
	return reason, reason != ""
}

func (s prefixedScope) AdmitFile(rel, abs string) bool {
	return selectedCatalogPath(s.rel(rel), false, s.selectedPaths) && s.scope.AdmitFile(s.rel(rel), abs)
}

func (s prefixedScope) DeferDir(rel, abs string) bool { return s.scope.DeferDir(s.rel(rel), abs) }

func (s prefixedScope) CollapseDir(rel, abs string) bool {
	return s.scope.CollapseDir(s.rel(rel), abs)
}

// policyFor is the catalog plane's policy for one root.
func (c *TreeStores) policyFor(ctx context.Context, root string) walkPolicy {
	c.scopesMu.RLock()
	scopes := c.scopes
	c.scopesMu.RUnlock()
	if scopes == nil {
		return defaultCatalogPolicy(root)
	}
	scope := scopes.Catalog(ctx, root)
	if scope == nil {
		return defaultCatalogPolicy(root)
	}
	return walkPolicy{budgets: scope.Budgets(), scope: scope, base: "."}
}

var (
	bundledPlaneOnce sync.Once
	bundledPlane     sourcescope.Plane
)

// Unreadable bundled budgets leave catalog walks unbounded.
func defaultCatalogPolicy(root string) walkPolicy {
	bundledPlaneOnce.Do(func() {
		cfg, err := sourcescope.DefaultConfig()
		if err != nil {
			slog.Warn("bundled source scope unreadable; catalog walks run unbounded", "error", err)
			return
		}
		bundledPlane = cfg.Catalog
	})
	scope := sourcescope.New(root, sourcescope.Options{Plane: bundledPlane})
	return walkPolicy{budgets: scope.Budgets(), scope: scope, base: "."}
}

func (p walkPolicy) boundaryDir(rel string) string {
	if p.scope == nil || p.includeDependencies {
		return ""
	}
	return p.scope.BoundaryDir(prefixedScope{base: p.base, scope: p.scope}.rel(rel))
}
func (p walkPolicy) boundaryPath(rel string, isDir bool) string {
	if p.scope == nil || p.includeDependencies {
		return ""
	}
	return p.scope.BoundaryPath(prefixedScope{base: p.base, scope: p.scope}.rel(rel), isDir)
}

func (p walkPolicy) selects(rel string, isDir bool) bool {
	full := prefixedScope{base: p.base}.rel(rel)
	if !selectedCatalogPath(full, isDir, p.selectedPaths) {
		return false
	}
	return !p.includeDependencies || p.scope == nil || p.scope.AdmitPath(full, isDir)
}

func selectedCatalogPath(rel string, isDir bool, selected []string) bool {
	if len(selected) == 0 {
		return true
	}
	for _, target := range selected {
		if rel == target || strings.HasPrefix(rel, target+"/") || isDir && (rel == "." || strings.HasPrefix(target, rel+"/")) {
			return true
		}
	}
	return false
}
