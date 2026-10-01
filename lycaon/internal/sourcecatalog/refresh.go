package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/repochange"
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
	ctx, release, err := admitMetadata(ctx, c.broker, backgroundwork.Request{
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
