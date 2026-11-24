package native

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/gitrepo"
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

type rootMutationBatch struct {
	resync bool
	paths  []string
	seen   map[string]struct{}
}

func (b *rootMutationBatch) add(rel string) {
	if rel == "" || rel == "." {
		b.resync = true
		return
	}
	if b.seen == nil {
		b.seen = map[string]struct{}{}
	}
	if _, ok := b.seen[rel]; !ok {
		b.seen[rel] = struct{}{}
		b.paths = append(b.paths, rel)
	}
}

// notifyWorktreeChangedLocations routes attached-root changes to repochange and
// invalidates search observations on registered linked checkouts without advancing
// attached-root generations or registering external credential watchers.
func notifyWorktreeChangedLocations(ctx context.Context, tctx tools.ToolContext, locations ...mutationLocation) {
	attached := make(map[string]*rootMutationBatch, len(locations))
	var attachedOrder []string
	recordAttached := func(rootPath, rel string) {
		if rootPath == "" {
			return
		}
		batch := attached[rootPath]
		if batch == nil {
			batch = &rootMutationBatch{}
			attached[rootPath] = batch
			attachedOrder = append(attachedOrder, rootPath)
		}
		batch.add(rel)
	}

	linked := make(map[string]*rootMutationBatch)
	var linkedOrder []string
	recordLinked := func(checkoutBase, rel string) {
		if checkoutBase == "" {
			return
		}
		batch := linked[checkoutBase]
		if batch == nil {
			batch = &rootMutationBatch{}
			linked[checkoutBase] = batch
			linkedOrder = append(linkedOrder, checkoutBase)
		}
		batch.add(rel)
	}

	candidates := candidateAttachedRoots(tctx)
	var resolver *gitrepo.CheckoutResolver
	resolverBuilt := false

	for _, loc := range locations {
		if loc.rootID != "" {
			if rootPath := mutationRootPath(tctx, loc.rootID); rootPath != "" {
				if rel, ok := classifyRootedLocation(rootPath, loc); ok {
					recordAttached(rootPath, rel)
					continue
				}
			}
		}
		if !filepath.IsAbs(loc.abs) {
			continue
		}
		if rootPath, rel, ok := matchCandidateRoot(candidates, loc.abs); ok {
			recordAttached(rootPath, rel)
			continue
		}
		if !resolverBuilt {
			resolver = gitrepo.NewCheckoutResolver(attachedGitRoots(tctx))
			resolverBuilt = true
		}
		if addr, ok := resolver.ResolvePath(loc.abs); ok {
			recordLinked(addr.RootPath, addr.Rel)
		}
	}

	for _, rootPath := range attachedOrder {
		batch := attached[rootPath]
		if batch.resync {
			notifyWorktreeRootResync(ctx, rootPath)
			continue
		}
		notifyWorktreeRoot(ctx, rootPath, batch.paths...)
	}
	for _, checkoutBase := range linkedOrder {
		batch := linked[checkoutBase]
		if batch.resync {
			sourcecatalog.Process().InvalidateRoot(checkoutBase) //nolint:contextcheck // Catalog refresh outlives the tool call.
			continue
		}
		sourcecatalog.Process().InvalidateRoot(checkoutBase, batch.paths...) //nolint:contextcheck // Catalog refresh outlives the tool call.
	}
}

func classifyRootedLocation(rootPath string, loc mutationLocation) (string, bool) {
	rawRel := strings.TrimSpace(loc.scopeRel)
	if rawRel != "" {
		if filepath.IsAbs(rawRel) {
			return relWithinAttachedRoot(rootPath, rawRel)
		}
		clean := filepath.Clean(filepath.FromSlash(rawRel))
		if clean == "." {
			return ".", true
		}
		if filepath.IsLocal(clean) {
			return filepath.ToSlash(clean), true
		}
		return "", false
	}
	if loc.abs == "" {
		return ".", true
	}
	if filepath.IsAbs(loc.abs) {
		return relWithinAttachedRoot(rootPath, loc.abs)
	}
	return "", false
}

func candidateAttachedRoots(tctx tools.ToolContext) []string {
	var out []string
	seen := map[string]struct{}{}
	push := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	if branch := strings.TrimSpace(tctx.Source.WorkerBranchRoot); branch != "" {
		push(branch)
		return out
	}
	for _, root := range tctx.Source.Roots {
		if !root.External {
			push(root.Path)
		}
	}
	push(tools.HostWriteRoot(tctx))
	return out
}

func matchCandidateRoot(roots []string, absPath string) (string, string, bool) {
	var bestRoot, bestRel string
	for _, root := range roots {
		rel, ok := relWithinAttachedRoot(root, absPath)
		if !ok {
			continue
		}
		if bestRoot == "" || len(root) > len(bestRoot) {
			bestRoot, bestRel = root, rel
		}
	}
	return bestRoot, bestRel, bestRoot != ""
}

func relWithinAttachedRoot(rootPath, targetPath string) (string, bool) {
	rootClean := filepath.Clean(rootPath)
	targetClean := filepath.Clean(targetPath)
	if rootClean == targetClean {
		return ".", true
	}
	if rel, err := filepath.Rel(rootClean, targetClean); err == nil && rel != "." && filepath.IsLocal(rel) {
		return filepath.ToSlash(rel), true
	}
	rootCanon := gitrepo.CanonicalDir(rootPath)
	targetCanon := gitrepo.CanonicalDir(targetPath)
	if rootCanon == "" || targetCanon == "" {
		return "", false
	}
	if rootCanon == targetCanon {
		return ".", true
	}
	if rel, err := filepath.Rel(rootCanon, targetCanon); err == nil && rel != "." && filepath.IsLocal(rel) {
		return filepath.ToSlash(rel), true
	}
	return "", false
}

func attachedGitRoots(tctx tools.ToolContext) []gitrepo.AttachedRoot {
	var roots []gitrepo.AttachedRoot
	for _, root := range tctx.Source.Roots {
		if root.External || strings.TrimSpace(root.ID) == "" || strings.TrimSpace(root.Path) == "" {
			continue
		}
		roots = append(roots, gitrepo.AttachedRoot{ID: root.ID, Path: root.Path})
	}
	return roots
}

// mutationRootPath maps a root id onto the host path the catalog indexes for it.
func mutationRootPath(tctx tools.ToolContext, rootID string) string {
	if rootID == projectroot.WorkerBranchRootID {
		return strings.TrimSpace(tctx.Source.WorkerBranchRoot)
	}
	for _, root := range tctx.Source.Roots {
		if !root.External && root.ID == rootID {
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
		if filepath.IsAbs(p) {
			rel, ok := relWithinAttachedRoot(rootPath, p)
			if !ok {
				continue
			}
			if rel == "." {
				notifyWorktreeRootResync(ctx, rootPath)
				return
			}
			cleaned = append(cleaned, rel)
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(p)))
		if clean == "" || clean == "." {
			continue
		}
		if !filepath.IsLocal(clean) {
			continue
		}
		cleaned = append(cleaned, filepath.ToSlash(clean))
	}
	if len(cleaned) == 0 {
		notifyWorktreeRootResync(ctx, rootPath)
		return
	}
	sourcecatalog.Process().InvalidateRoot(rootPath, cleaned...) //nolint:contextcheck // Catalog refresh outlives the tool call.
	repochange.NotifyWorktreeDebounced(ctx, rootPath, cleaned, repochange.SourceMutation)
}

func notifyWorktreeRootResync(ctx context.Context, rootPath string) {
	sourcecatalog.Process().InvalidateRoot(rootPath) //nolint:contextcheck // Catalog refresh outlives the tool call.
	repochange.NotifyWorktreeChangesDebounced(ctx, rootPath, []repochange.WorktreeChange{{Path: ".", Kind: repochange.WorktreeChangeResync}}, repochange.SourceMutation)
}
