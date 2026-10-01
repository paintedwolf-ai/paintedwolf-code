package sourcecomparison

import (
	"context"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/pkg/api"
)

// A measurement is a few counts, so many are held for the cost of one document.
const measureEntries = 16384
const measureBytes = 8 << 20
const measureEntryBytes = 256

// Measurement is what a comparison counts before anyone reads it: the lines it
// changed and the rows its display would occupy.
type Measurement struct {
	Summary api.SourceComparisonSummary
	Extent  DisplayExtent
}

// Measure counts a comparison without decorating or retaining its document.
// Measurements are keyed by content identity within the project, so they never
// need invalidating; a document already prepared for a reader answers directly.
func (c *Cache) Measure(ctx context.Context, owner, project string, before, after api.SourceComparisonSide,
	attribution *api.SourceComparisonAttribution, mode string, priority backgroundwork.Priority,
) (Measurement, error) {
	c.initialize()
	before.SecretScreen, after.SecretScreen = nil, nil
	pair, err := comparisonKey(owner, project, before, after, attribution)
	if err != nil {
		return Measurement{}, err
	}
	key := pair + "\x00" + mode
	c.measureMu.Lock()
	held, ok := c.measures.Get(key)
	c.measureMu.Unlock()
	if ok {
		return held, nil
	}
	measured, err := c.measurements.Do(ctx, key, func(work context.Context) (Measurement, error) {
		if answer, ok := c.measureRetained(work, pair, owner, project, mode); ok {
			return answer, nil
		}
		admission, err := backgroundwork.Process().Acquire(work, backgroundwork.Request{Resources: []backgroundwork.Resource{backgroundwork.ResourceCPU}, Priority: priority})
		if err != nil {
			return Measurement{}, err
		}
		defer admission()
		document, err := New(before, after, attribution)
		if err != nil {
			return Measurement{}, err
		}
		extent, err := document.DisplayExtent(work, mode)
		if err != nil {
			return Measurement{}, err
		}
		return Measurement{Summary: document.Summary, Extent: extent}, nil
	})
	if err != nil {
		return Measurement{}, err
	}
	c.measureMu.Lock()
	c.measures.Put(key, measured, measureEntryBytes)
	c.measureMu.Unlock()
	return measured, nil
}

// measureRetained counts from a document a reader already prepared.
func (c *Cache) measureRetained(ctx context.Context, pair, owner, project, mode string) (Measurement, bool) {
	c.mu.Lock()
	id := c.keys[pair]
	c.mu.Unlock()
	if id == "" {
		return Measurement{}, false
	}
	retained, release, err := c.resources.Acquire(pagedview.Scope{Person: owner, Project: project}, id)
	if err != nil || retained.document == nil {
		return Measurement{}, false
	}
	defer release()
	extent, err := retained.document.DisplayExtent(ctx, mode)
	if err != nil {
		return Measurement{}, false
	}
	return Measurement{Summary: retained.document.Summary, Extent: extent}, true
}
