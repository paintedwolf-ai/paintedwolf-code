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

// mutationLocation carries the structured identity of a mutated path so
// invalidation never re-resolves display strings. Display keeps traveling for
// the legacy notifiers; the catalog hears (root, scope-relative) directly.
type mutationLocation struct {
	rootID   string
	scopeRel string
	display  string
}

func resolvedMutationLocation(res projectpaths.Resolved) mutationLocation {
	return mutationLocation{rootID: res.Root.ID, scopeRel: res.ScopeRel, display: res.DisplayPath}
}

// afterSuccessfulMutation runs worker mutation hooks, emits WorktreeChanged,
// and notifies the blueprint write observer. Paths arrive in the model path
// grammar (bare, @label-qualified, or absolute).
func afterSuccessfulMutation(ctx context.Context, tctx tools.ToolContext, paths ...string) {
	tools.ReportSyntaxOverride(ctx, tctx, paths...)
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		afterWorkerMutation(ctx, tctx, p)
	}
	notifyWorktreeChanged(ctx, tctx, paths...)
	notifyBlueprintWrite(ctx, tctx, paths...)
}

// afterSuccessfulMutationResolved reports one resolved mutation: legacy
// notifiers keep the display spelling while invalidation carries identity.
func afterSuccessfulMutationResolved(ctx context.Context, tctx tools.ToolContext, res projectpaths.Resolved) {
	afterSuccessfulMutationLocations(ctx, tctx, resolvedMutationLocation(res))
}

// afterSuccessfulMutationLocations is the structured form of
// afterSuccessfulMutation for callers holding resolved identities.
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

// notifyWorktreeChanged debounces WorktreeChanged events for the mutated
// paths. Each path is resolved once against the session's root set so the
// event carries (root, scope-relative) locations; paths outside the root
// grammar keep the write-root join.
func notifyWorktreeChanged(ctx context.Context, tctx tools.ToolContext, paths ...string) {
	locations := make([]mutationLocation, 0, len(paths))
	for _, p := range paths {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if abs, root, err := projectroot.ResolveAbs(tctx.Source.Roots, tctx.Source.ActiveRootID, p); err == nil {
			locations = append(locations, mutationLocation{rootID: root.ID, scopeRel: projectroot.ScopeRel(root, abs), display: p})
			continue
		}
		locations = append(locations, mutationLocation{display: p})
	}
	notifyWorktreeChangedLocations(ctx, tctx, locations...)
}

// notifyWorktreeChangedLocations groups mutated paths by their structured root
// identity and debounces one WorktreeChanged event per affected root.
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
		// No structured identity for this location (external areas, grants):
		// keep the display spelling under the write root, as before.
		add(fallbackRoot, loc.display)
	}
	for _, rootPath := range order {
		notifyWorktreeRoot(ctx, rootPath, grouped[rootPath]...)
	}
}

// mutationRootPath maps a structured root id onto the host path the catalog
// indexes for it.
func mutationRootPath(tctx tools.ToolContext, rootID string) string {
	if rootID == "worker-branch" {
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
// root. Paths are scope-relative under the root or absolute.
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
