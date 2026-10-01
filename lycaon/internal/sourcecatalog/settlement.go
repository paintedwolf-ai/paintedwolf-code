package sourcecatalog

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
)

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
