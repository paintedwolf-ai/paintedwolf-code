package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
)

type claimResult struct {
	snapshot Snapshot
	building bool
	build    bool
	done     <-chan struct{}
	context  context.Context
}

// snapshotRoot joins discovery without owning its lifetime.
func (c *Catalog) snapshotRoot(ctx context.Context, projectID string, root Root) (Snapshot, error) {
	key := rootKey(projectID, root)
	for range maxRootBuildAttempts {
		claim := c.claim(ctx, key, projectID, []Root{root})
		if claim.build {
			go c.runRootBuild(claim.context, projectID, root, key, 0) //nolint:contextcheck // claim derives and registers this cancelable context from ctx.
		}
		if claim.building || claim.build {
			select {
			case <-ctx.Done():
				return c.snapshotAfterCancel(key, root, ctx.Err())
			case <-claim.done:
			}
		}
		published := c.published(key)
		if published.State == StateFailed {
			return published, errors.New(published.Error)
		}
		if published.State == StateReady {
			return published, nil
		}
	}
	return c.published(key), nil
}

// runRootBuild detaches admitted walks from request cancellation.
func (c *Catalog) runRootBuild(ctx context.Context, projectID string, root Root, key string, maxWait time.Duration) {
	priority := backgroundwork.PriorityInteractive
	if maxWait > 0 {
		priority = backgroundwork.PriorityProactive
	}
	started := time.Now()
	snapshot, buildErr := c.buildRoot(
		ctx,
		projectID,
		root,
		priority,
		maxWait,
	)
	if buildErr != nil {
		slog.WarnContext(ctx, "source catalog root walk failed",
			"root", root.Path, "duration_ms", time.Since(started).Milliseconds(), "err", buildErr)
	} else {
		slog.InfoContext(ctx, "source catalog root walked",
			"root", root.Path, "duration_ms", time.Since(started).Milliseconds(),
			"entries", len(snapshot.Entries), "moving", snapshot.Moving)
	}
	c.publish(key, snapshot, buildErr)
}

func (c *Catalog) snapshotAfterCancel(key string, root Root, err error) (Snapshot, error) {
	published := c.published(key)
	if published.State == StateReady {
		return published, nil
	}
	return Snapshot{}, fmt.Errorf("catalog root %s: %w", root.ID, err)
}

// Current returns the last complete generation immediately and starts refresh
// work when the catalog is cold or stale.
func (c *Catalog) Current(ctx context.Context, projectID string, roots []Root) Snapshot {
	if c == nil {
		return Snapshot{State: StateFailed, Error: "source catalog is nil"}
	}
	cleaned, err := cleanRoots(roots)
	if err != nil {
		return Snapshot{State: StateFailed, Error: err.Error()}
	}
	parts := make([]Snapshot, 0, len(cleaned))
	for _, root := range cleaned {
		parts = append(parts, c.currentRoot(ctx, projectID, root))
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return combineSnapshots(cleaned, parts)
}

func (c *Catalog) currentRoot(ctx context.Context, projectID string, root Root) Snapshot {
	key := rootKey(projectID, root)
	claim := c.claim(ctx, key, projectID, []Root{root})
	if claim.build {
		go c.runRootBuild(claim.context, projectID, root, key, detachedRefreshMaxWait) //nolint:contextcheck // claim derives and registers this cancelable context from ctx.
	}
	out := claim.snapshot
	out.Refreshing = claim.building || claim.build
	if out.State == "" {
		out.State = StateWarming
		out.Roots = []Root{root}
	}
	return out
}

// unresolvedBuild reports errors that say nothing about the tree itself.
func unresolvedBuild(err error) bool {
	return errors.Is(err, backgroundwork.ErrAcquireTimeout) ||
		errors.Is(err, backgroundwork.ErrSuperseded) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

func (c *Catalog) buildRoot(
	ctx context.Context,
	projectID string,
	root Root,
	priority backgroundwork.Priority,
	maxWait time.Duration,
) (Snapshot, error) {
	epoch := repochange.CurrentEpoch(root.Path)
	ctx, release, err := admitMetadata(ctx, c.Trees.broker, backgroundwork.Request{
		Key: projectID + ":" + root.ID + ":catalog", Epoch: epoch.Value, MaxWait: maxWait,
		// Each generation has one metadata lane.
		Lane:     root.Path,
		Priority: priority, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata},
	})
	if err != nil {
		return Snapshot{Roots: []Root{root}}, err
	}
	defer release()
	epoch = repochange.CurrentEpoch(root.Path)
	snapshot, err := c.reconcile(ctx, projectID, root)
	if err != nil {
		return snapshot, err
	}
	if !repochange.EpochCurrent(root.Path, epoch) {
		// Changes this catalog classified during the walk are pending in the
		// record; only an unclassified advance leaves the generation unpinned.
		observed := c.observedEpoch(rootKey(projectID, root))
		if !repochange.EpochCurrent(root.Path, observed) {
			snapshot.Moving = true
			snapshot.Epochs = nil
			return snapshot, nil
		}
		epoch = observed
	}
	snapshot.Epochs = map[string]repochange.Epoch{root.ID: epoch}
	return snapshot, nil
}

func (c *Catalog) observedEpoch(key string) repochange.Epoch {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rec := c.records[key]; rec != nil {
		return rec.observed
	}
	return repochange.Epoch{}
}

func (c *Catalog) claim(ctx context.Context, key, projectID string, roots []Root) claimResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	rec := c.records[key]
	if rec == nil {
		rec = &record{
			projectID: projectID, signature: key, roots: append([]Root(nil), roots...),
			scoped: scopedRoots(roots), stale: true,
			fullReconcile: true,
		}
		c.records[key] = rec
	}
	rec.lastUsed = now
	if rec.building || (!rec.stale && rec.snapshot.State == StateReady) {
		return claimResult{snapshot: rec.snapshot, building: rec.building, done: rec.done}
	}
	rec.building = true
	rec.done = make(chan struct{})
	buildCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	rec.cancel = cancel
	c.evictLocked(key)
	return claimResult{snapshot: rec.snapshot, building: true, build: true, done: rec.done, context: buildCtx}
}

func (c *Catalog) publish(key string, snapshot Snapshot, buildErr error) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec := c.records[key]
	if rec == nil || !rec.building {
		return snapshot
	}
	if buildErr == nil && (rec.fullReconcile || len(rec.dirtyPaths) > 0) {
		snapshot.Moving = true
	}
	if buildErr == nil && snapshot.Moving {
		// Waiters observe the moving generation. The changes that overtook the
		// walk stay pending, so the next pass reconciles only those paths.
		c.revision++
		snapshot.State = StateReady
		snapshot.Revision = c.revision
		snapshot.Refreshing = false
		snapshot.Moving = true
		snapshot.Epochs = nil
		rec.snapshot = snapshot
		rec.bytes = snapshot.retainedBytes()
		rec.stale = true
		rec.mustAdvanceRevision = true
	} else if buildErr != nil {
		if rec.snapshot.State != StateReady && !unresolvedBuild(buildErr) {
			snapshot.State = StateFailed
			snapshot.Error = buildErr.Error()
			rec.snapshot = snapshot
		}
		rec.stale = true
		rec.fullReconcile = true
	} else {
		if !rec.mustAdvanceRevision && sameCatalogMaterial(rec.snapshot, snapshot) {
			snapshot.Revision = rec.snapshot.Revision
		} else {
			c.revision++
			snapshot.Revision = c.revision
		}
		snapshot.State = StateReady
		snapshot.Refreshing = false
		rec.snapshot = snapshot
		rec.bytes = snapshot.retainedBytes()
		rec.stale = false
		rec.mustAdvanceRevision = false
		rec.validatedAt = c.now()
	}
	rec.building = false
	rec.cancel()
	close(rec.done)
	c.evictLocked(key)
	return rec.snapshot
}

func sameCatalogMaterial(left, right Snapshot) bool {
	return left.State == StateReady && left.Revision != 0 &&
		slices.Equal(left.Roots, right.Roots) &&
		slices.Equal(left.Entries, right.Entries) &&
		maps.Equal(left.Epochs, right.Epochs)
}

func (c *Catalog) recentlyValidated(projectID string, root Root, maxAge time.Duration) bool {
	if c == nil || maxAge <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rec := c.records[rootKey(projectID, root)]
	return rec != nil && !rec.validatedAt.IsZero() && c.now().Sub(rec.validatedAt) < maxAge
}

// published returns the generation currently held for key, zero when cold.
func (c *Catalog) published(key string) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rec := c.records[key]; rec != nil {
		return rec.snapshot
	}
	return Snapshot{}
}

// incompleteCoverageTTL bounds generation reuse without complete watcher coverage.
const incompleteCoverageTTL = 2 * time.Second

// CurrentSettled reports freshness and serves the published generation during reconciliation.
func (c *Catalog) CurrentSettled(ctx context.Context, projectID string, roots []Root) (Snapshot, bool) {
	current, settled, _ := c.currentGeneration(ctx, projectID, roots)
	return current, settled
}

func (c *Catalog) currentGeneration(ctx context.Context, projectID string, roots []Root) (Snapshot, bool, bool) {
	current, settled, servable, reconcile := c.currentSettlement(ctx, projectID, roots)
	for _, request := range reconcile {
		c.requestReconcile(projectID, request.root, request.full)
	}
	if !settled {
		// Serve the complete generation during reconciliation.
		current = c.Current(ctx, projectID, roots)
	}
	return current, settled, servable
}

// Observe joins cold or invalidated builds and otherwise serves the completed generation.
func (c *Catalog) Observe(ctx context.Context, projectID string, roots []Root) (Snapshot, error) {
	current, _, servable := c.currentGeneration(ctx, projectID, roots)
	if servable && current.State == StateReady {
		return current, nil
	}
	return c.Snapshot(ctx, projectID, roots)
}

// ObserveWithin serves the last complete generation, waiting at most grace for
// the build replacing an unservable one; the bool reports whether it is current.
// The build outlives the wait. With no complete generation, the error wraps
// the expired wait.
func (c *Catalog) ObserveWithin(ctx context.Context, projectID string, roots []Root, grace time.Duration) (Snapshot, bool, error) {
	current, _, servable := c.currentGeneration(ctx, projectID, roots)
	if servable && current.State == StateReady {
		return current, true, nil
	}
	joinCtx, cancel := context.WithTimeout(ctx, grace)
	defer cancel()
	snapshot, err := c.Snapshot(joinCtx, projectID, roots)
	if err != nil {
		return Snapshot{}, false, err
	}
	return snapshot, joinCtx.Err() == nil, nil
}

// reconcileRequest names a root whose generation needs another pass; full
// re-walks it instead of reconciling the pending changes.
type reconcileRequest struct {
	root Root
	full bool
}

func (c *Catalog) currentSettlement(ctx context.Context, projectID string, roots []Root) (Snapshot, bool, bool, []reconcileRequest) {
	cleaned, err := cleanRoots(roots)
	if err != nil {
		requests := make([]reconcileRequest, 0, len(roots))
		for _, root := range roots {
			requests = append(requests, reconcileRequest{root: root, full: true})
		}
		return c.Current(ctx, projectID, roots), false, false, requests
	}
	parts := make([]Snapshot, 0, len(cleaned))
	settled := true
	servable := true
	reconcile := make([]reconcileRequest, 0, len(cleaned))
	for _, root := range cleaned {
		part := c.Current(ctx, projectID, []Root{root})
		parts = append(parts, part)
		if part.State != StateReady {
			settled = false
			servable = false
			if !part.Refreshing {
				reconcile = append(reconcile, reconcileRequest{root: root, full: true})
			}
			continue
		}
		if part.Refreshing || part.Moving {
			settled = false
			if !part.Moving {
				servable = false
			}
			if !part.Refreshing {
				reconcile = append(reconcile, reconcileRequest{root: root})
			}
			continue
		}
		complete := repochange.Coverage(root.watchRoot()).Complete
		epoch, ok := part.Epochs[root.ID]
		if !ok || !repochange.EpochCurrent(root.Path, epoch) {
			// With complete coverage the watcher has delivered, or is delivering,
			// every change behind the advance; they decide what is reconciled.
			settled = false
			servable = false
			reconcile = append(reconcile, reconcileRequest{root: root, full: !complete})
			continue
		}
		if !complete && !c.recentlyValidated(projectID, root, incompleteCoverageTTL) {
			settled = false
			servable = false
			reconcile = append(reconcile, reconcileRequest{root: root, full: true})
		}
	}
	if len(parts) == 1 {
		return parts[0], settled, servable, reconcile
	}
	current := combineSnapshots(cleaned, parts)
	return current, settled, servable, reconcile
}

func (c *Catalog) reconcile(ctx context.Context, projectID string, root Root) (Snapshot, error) {
	c.mu.Lock()
	rec := c.records[rootKey(projectID, root)]
	base := rec.snapshot
	full := rec.fullReconcile || base.State != StateReady
	paths := make([]string, 0, len(rec.dirtyPaths))
	for p := range rec.dirtyPaths {
		paths = append(paths, p)
	}
	rec.dirtyPaths = nil
	rec.fullReconcile = false
	c.mu.Unlock()
	policy := c.Trees.policyFor(ctx, root.Path)
	if full || !repochange.Coverage(root.watchRoot()).Complete {
		return c.build(ctx, []Root{root}, policy)
	}
	if len(paths) == 0 {
		// The watcher delivered nothing that touches this tree since base.
		base.Moving = false
		return base, nil
	}
	return reconcilePaths(ctx, root, base, paths, policy)
}

func reconcilePaths(ctx context.Context, root Root, base Snapshot, changed []string, policy walkPolicy) (Snapshot, error) {
	paths, ok := reconciliationPaths(root, changed)
	if !ok {
		return buildSnapshot(ctx, []Root{root}, policy)
	}
	entries := make(map[string]Entry, len(base.Entries))
	for _, entry := range base.Entries {
		entries[entry.Path] = entry
	}
	reconciled := make(map[string]bool, len(paths))
	for _, rel := range paths {
		if err := nextMetadataEntry(ctx); err != nil {
			return Snapshot{}, err
		}
		if sandbox.ShouldSkipDir(rel, path.Base(rel)) || repochange.IsPrivatePath(filepath.Join(root.Path, filepath.FromSlash(rel))) {
			continue
		}
		// Ancestor checks reject replacements before descendant access.
		parts := strings.Split(rel, "/")
		for depth := 1; depth < len(parts); depth++ {
			parent := strings.Join(parts[:depth], "/")
			info, err := os.Lstat(filepath.Join(root.Path, filepath.FromSlash(parent)))
			if errors.Is(err, os.ErrNotExist) || (err == nil && !info.IsDir()) {
				rel = parent
				break
			}
			if err != nil {
				return Snapshot{}, err
			}
			entries[parent] = catalogEntry(root.ID, parent, info, root.Path)
		}
		covered := false
		for parent := rel; parent != "."; parent = path.Dir(parent) {
			if reconciled[parent] {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		if base.underBoundary(root.ID, rel) {
			// Unobserved descendants cannot change this generation.
			continue
		}
		reconciled[rel] = true
		removeCatalogSubtree(entries, base, root.ID, rel)
		abs := filepath.Join(root.Path, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Snapshot{}, err
		}
		if sandbox.ShouldSkipDir(rel, path.Base(rel)) {
			continue
		}
		entries[rel] = catalogEntry(root.ID, rel, info, root.Path)
		if !info.IsDir() {
			continue
		}
		subtree, err := buildSnapshot(ctx, []Root{{ID: root.ID, Path: abs}}, policy.under(rel))
		if err != nil {
			return Snapshot{}, err
		}
		if subtree.RootBoundary {
			bounded := entries[rel]
			bounded.Boundary = true
			entries[rel] = bounded
		}
		for _, entry := range subtree.Entries {
			entry.Path = path.Join(rel, entry.Path)
			entry.Parent = normalizeDir(path.Dir(entry.Path))
			entry.Depth = pathDepth(entry.Path)
			entries[entry.Path] = entry
		}
	}
	return snapshotFromEntries(root, entries), nil
}

func removeCatalogSubtree(entries map[string]Entry, base Snapshot, rootID, rel string) {
	pending := []string{rel}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		delete(entries, current)
		for _, index := range base.children[entryKey(rootID, current)] {
			pending = append(pending, base.Entries[index].Path)
		}
	}
}

func reconciliationPaths(root Root, changed []string) ([]string, bool) {
	paths := make([]string, 0, len(changed))
	for _, p := range changed {
		if filepath.IsAbs(p) {
			var err error
			p, err = filepath.Rel(root.Path, p)
			if err != nil {
				return nil, false
			}
		}
		p = filepath.ToSlash(filepath.Clean(p))
		if p == "." || p == ".." || strings.HasPrefix(p, "../") {
			return nil, false
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := paths[:0]
	for _, p := range paths {
		if len(out) > 0 && (p == out[len(out)-1] || strings.HasPrefix(p, out[len(out)-1]+"/")) {
			continue
		}
		out = append(out, p)
	}
	return out, true
}

func catalogEntry(rootID, rel string, info os.FileInfo, rootPath string) Entry {
	e := Entry{RootID: rootID, Path: rel, Parent: normalizeDir(path.Dir(rel)), Name: path.Base(rel),
		Depth: pathDepth(rel), IsDir: info.IsDir(), IsSymlink: info.Mode()&os.ModeSymlink != 0,
		Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime().UTC()}
	abs := filepath.Join(rootPath, filepath.FromSlash(rel))
	if e.IsDir {
		e.IsVCSRoot = gitrepo.IsRoot(abs)
	}
	if e.IsSymlink {
		if target, err := os.Stat(abs); err == nil {
			e.TargetIsDir = target.IsDir()
		}
	}
	return e
}

func snapshotFromEntries(root Root, entries map[string]Entry) Snapshot {
	ordered := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	return NewSnapshot([]Root{root}, ordered)
}
