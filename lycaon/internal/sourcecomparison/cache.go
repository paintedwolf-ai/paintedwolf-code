package sourcecomparison

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/pkg/api"
)

const cacheEntries = 1024
const cacheLifetime = 30 * time.Minute

type retainedComparison struct{ document *Document }

// Cache shares immutable preparation within an authenticated project boundary.
// Callers pin documents for the duration of their reads.
type Cache struct {
	once      sync.Once
	mu        sync.Mutex
	resources *pagedview.Registry[*retainedComparison]
	keys      map[string]string
	pending   pagedview.Preparation[string, string]
	budget    *pagedview.Budget
	// Measurements are counts, not documents: they outlive the budget that
	// bounds retained plans and are held in their own small store.
	measureMu    sync.Mutex
	measures     *pagedview.Cache[string, Measurement]
	measurements pagedview.Preparation[string, Measurement]
}

func NewCache(budget *pagedview.Budget) *Cache { return &Cache{budget: budget} }
func (c *Cache) initialize() {
	c.once.Do(func() {
		if c.budget == nil {
			c.budget = pagedview.NewBudget(128 << 20)
		}
		c.resources = pagedview.NewRegistry[*retainedComparison](c.budget, cacheEntries, cacheLifetime)
		c.keys = make(map[string]string)
		c.measures = pagedview.NewCache[string, Measurement](measureEntries, measureBytes)
	})
}

func comparisonKey(owner, project string, before, after api.SourceComparisonSide, attribution *api.SourceComparisonAttribution) (string, error) {
	// Paths and authors affect rendering; version labels do not.
	for _, side := range []*api.SourceComparisonSide{&before, &after} {
		side.Content = Hash(side.Content)
		side.VersionID, side.RootID, side.Reason = "", "", ""
	}
	raw, err := json.Marshal(struct {
		Owner, Project string
		Before, After  api.SourceComparisonSide
		Attribution    *api.SourceComparisonAttribution
	}{owner, project, before, after, attribution})
	return Hash(string(raw)), err
}

func (c *Cache) Prepare(ctx context.Context, owner, project string, before, after api.SourceComparisonSide, attribution *api.SourceComparisonAttribution) (*Document, func(), error) {
	c.initialize()
	before.SecretScreen, after.SecretScreen = nil, nil
	key, err := comparisonKey(owner, project, before, after, attribution)
	if err != nil {
		return nil, nil, err
	}
	scope := pagedview.Scope{Person: owner, Project: project}
	c.mu.Lock()
	id := c.keys[key]
	c.mu.Unlock()
	if id != "" {
		held, release, err := c.resources.Acquire(scope, id)
		if err == nil {
			return held.document, release, nil
		}
	}
	id, err = c.pending.Do(ctx, key, func(work context.Context) (string, error) {
		c.mu.Lock()
		existing := c.keys[key]
		c.mu.Unlock()
		if existing != "" {
			_, release, err := c.resources.Acquire(scope, existing)
			if err == nil {
				release()
				return existing, nil
			}
		}
		held := &retainedComparison{}
		id, release, err := c.resources.PutPinned(scope, held, comparisonReservation(before, after, attribution), func(held *retainedComparison) {
			if held.document != nil {
				held.document.decorationWork.Close()
			}
		})
		if err != nil {
			return "", err
		}
		defer release()
		admission, err := backgroundwork.Process().Acquire(work, backgroundwork.Request{Resources: []backgroundwork.Resource{backgroundwork.ResourceCPU}, Priority: backgroundwork.PriorityInteractive})
		if err != nil {
			c.resources.Release(scope, id)
			return "", err
		}
		defer admission()
		document, err := New(before, after, attribution)
		if document != nil {
			held.document = document
		}
		if err == nil {
			err = c.resources.Resize(scope, id, document.planReservation())
		}
		if err == nil {
			document.prepare()
			err = c.resources.Resize(scope, id, document.decorationReservation())
		}
		if err == nil {
			err = document.Decorate(work)
		}
		if err == nil {
			err = c.resources.Resize(scope, id, document.retainedBytes())
		}
		if err == nil {
			err = work.Err()
		}
		if err != nil {
			c.resources.Release(scope, id)
			return "", err
		}
		held.document = document
		c.mu.Lock()
		// The identity map is only a hint; the registry owns scope, expiry and bytes.
		if len(c.keys) >= cacheEntries*2 {
			clear(c.keys)
		}
		c.keys[key] = id
		c.mu.Unlock()
		return id, nil
	})
	if err != nil {
		return nil, nil, err
	}
	held, release, err := c.resources.Acquire(scope, id)
	if err != nil {
		return nil, nil, err
	}
	return held.document, release, nil
}

func (c *Cache) Close() {
	c.initialize()
	c.pending.Close()
	c.measurements.Close()
	c.resources.Close()
}

// Budget lets view descriptors and immutable resources share one retention limit.
func (c *Cache) Budget() *pagedview.Budget {
	c.initialize()
	return c.budget
}
func (c *Cache) Sweep() { c.initialize(); c.resources.Sweep() }
