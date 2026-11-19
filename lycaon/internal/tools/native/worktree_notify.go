package native

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// mutationLocation is one mutated path: its root identity, its scope-relative
// path, and the model's spelling for observers keyed on it.
type mutationLocation struct {
	rootID   string
	scopeRel string
	abs      string
	display  string
}

func resolvedMutationLocation(res projectpaths.Resolved) mutationLocation {
	return mutationLocation{rootID: res.Root.ID, scopeRel: res.ScopeRel, abs: res.Abs, display: res.DisplayPath}
}

// afterSuccessfulMutationResolved reports one resolved mutation.
func afterSuccessfulMutationResolved(ctx context.Context, tctx tools.ToolContext, res projectpaths.Resolved) {
	afterSuccessfulMutationLocations(ctx, tctx, resolvedMutationLocation(res))
}

// afterSuccessfulMutationLocations runs worker mutation hooks, emits
// WorktreeChanged, and notifies the blueprint write observer.
func afterSuccessfulMutationLocations(ctx context.Context, tctx tools.ToolContext, locations ...mutationLocation) {
	displays := make([]string, 0, len(locations))
	for _, loc := range locations {
		if display := strings.TrimSpace(loc.display); display != "" {
			displays = append(displays, display)
		}
	}
	tools.ReportSyntaxOverride(ctx, tctx, displays...)
	for _, display := range displays {
		afterWorkerMutation(ctx, tctx, display)
	}
	notifyWorktreeChangedLocations(ctx, tctx, locations...)
	notifyBlueprintWrite(ctx, tctx, displays...)
}

// notifyWorktreeChangedLocations groups mutated paths by root and debounces
// one WorktreeChanged event per affected root.
func notifyWorktreeChangedLocations(ctx context.Context, tctx tools.ToolContext, locations ...mutationLocation) {
	fallbackRoot := strings.TrimSpace(tools.HostWriteRoot(tctx))
	grouped := make(map[string][]string, len(locations))
	var order []string
	add := func(rootPath, path string) {
		if rootPath == "" {
			return
		}
		if _, seen := grouped[rootPath]; !seen {
			order = append(order, rootPath)
		}
		grouped[rootPath] = append(grouped[rootPath], path)
	}
	for _, loc := range locations {
		if loc.rootID != "" && loc.scopeRel != "" {
			if rootPath := mutationRootPath(tctx, loc.rootID); rootPath != "" {
				add(rootPath, loc.scopeRel)
				continue
			}
		}
		if fallbackRoot == "" || !filepath.IsAbs(loc.abs) {
			continue
		}
		if rel, err := filepath.Rel(fallbackRoot, loc.abs); err == nil && rel != "." && filepath.IsLocal(rel) {
			add(fallbackRoot, rel)
		}
	}
	for _, rootPath := range order {
		notifyWorktreeRoot(ctx, rootPath, grouped[rootPath]...)
	}
}

// mutationRootPath maps a root id onto the host path the catalog indexes for it.
func mutationRootPath(tctx tools.ToolContext, rootID string) string {
	if rootID == projectroot.WorkerBranchRootID {
		return strings.TrimSpace(tctx.Source.WorkerBranchRoot)
	}
	for _, root := range tctx.Source.Roots {
		if root.ID == rootID {
			return root.Path
		}
	}
	return ""
}

// notifyWorktreeRoot debounces invalidation and change notification for one
// root. Paths are scope-relative under the root.
func notifyWorktreeRoot(ctx context.Context, rootPath string, paths ...string) {
	cleaned := make([]string, 0, len(paths))
	for _, p := range paths {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" || p == "." {
			continue
		}
		cleaned = append(cleaned, p)
	}
	sourcecatalog.Process().InvalidateRoot(rootPath, cleaned...) //nolint:contextcheck // Catalog refresh outlives the tool call.
	repochange.NotifyWorktreeDebounced(ctx, rootPath, cleaned, repochange.SourceMutation)
}
