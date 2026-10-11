package sourcecatalog

import "context"

// Lanes run in order: source, then deferred trees a recursive expansion still
// opens, then the trees it leaves closed. Only the last may be unlisted when
// the lane boundary publishes.
type scanLane int

const (
	laneSource scanLane = iota
	laneLate
	laneCollapsed
)

type structuralScanFrontier struct {
	queues   [3]structuralScanQueue
	policy   walkPolicy
	draining scanLane
}

func newStructuralScanFrontier(ctx context.Context, root string, options structuralScanOptions) *structuralScanFrontier {
	var policy walkPolicy
	if options.store != nil {
		policy = options.store.stores.policyFor(ctx, root)
	} else {
		policy = defaultCatalogPolicy(root)
	}
	dir := structuralScanSpoolDir(options)
	q := &structuralScanFrontier{policy: policy}
	for i := range q.queues {
		q.queues[i].dir = dir
	}
	return q
}

func (q *structuralScanFrontier) len() int {
	total := 0
	for i := range q.queues {
		total += q.queues[i].len()
	}
	return total
}

func (q *structuralScanFrontier) ready(active int) bool {
	if q.queues[laneSource].len() > 0 {
		return true
	}
	for lane := laneLate; lane <= laneCollapsed; lane++ {
		// Active readers in an earlier lane may still discover its descendants.
		if q.queues[lane].len() > 0 && (q.draining >= lane || active == 0) {
			return true
		}
	}
	return false
}

// atLaneBoundary reports the one moment when every directory a recursive
// expansion opens has been listed and no collapsed tree has been read yet.
func (q *structuralScanFrontier) atLaneBoundary(active int) bool {
	return q.draining < laneCollapsed && active == 0 &&
		q.queues[laneSource].len() == 0 && q.queues[laneLate].len() == 0 &&
		q.queues[laneCollapsed].len() > 0
}

// laneOf keeps descendants of a later lane in it, so a source-shaped name
// inside a collapsed tree does not rejoin ordinary discovery.
func (q *structuralScanFrontier) laneOf(rel string) scanLane {
	lane := laneSource
	switch {
	case q.policy.collapseDir(rel):
		lane = laneCollapsed
	case q.policy.deferDir(rel):
		lane = laneLate
	}
	return max(lane, q.draining)
}

func (q *structuralScanFrontier) push(paths []string) error {
	for _, rel := range paths {
		if err := q.queues[q.laneOf(rel)].push([]string{rel}); err != nil {
			return err
		}
	}
	return nil
}

func (q *structuralScanFrontier) pop() (string, error) {
	for lane := laneSource; lane <= laneCollapsed; lane++ {
		if q.queues[lane].len() > 0 {
			q.draining = max(q.draining, lane)
			return q.queues[lane].pop()
		}
	}
	return q.queues[laneCollapsed].pop()
}

func (q *structuralScanFrontier) clearMemory() {
	for i := range q.queues {
		q.queues[i].clearMemory()
	}
}

func (q *structuralScanFrontier) close() {
	for i := range q.queues {
		q.queues[i].close()
	}
}
