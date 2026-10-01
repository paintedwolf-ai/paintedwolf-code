package sourcecatalog

import (
	"context"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
)

// entryOverheadBytes estimates each entry and child-index slot.
const entryOverheadBytes = 128

func (c *Catalog) evictLocked(keep string) {
	c.trimPoolLocked(keep, true, c.scopedLimit)
	c.trimPoolLocked(keep, false, c.rootLimit)
	if c.byteBudget <= 0 {
		return
	}
	for c.retainedBytesLocked() > c.byteBudget {
		if !c.evictOldestLocked(keep, scopedOnly) && !c.evictOldestLocked(keep, anyPool) {
			return
		}
	}
}

func (c *Catalog) trimPoolLocked(keep string, scoped bool, limit int) {
	if limit <= 0 {
		return
	}
	pool := func() int {
		count := 0
		for _, rec := range c.records {
			if rec.scoped == scoped {
				count++
			}
		}
		return count
	}
	for pool() > limit {
		if !c.evictOldestLocked(keep, poolFilter{match: true, scoped: scoped}) {
			return
		}
	}
}

type poolFilter struct {
	match  bool
	scoped bool
}

var (
	anyPool    = poolFilter{}
	scopedOnly = poolFilter{match: true, scoped: true}
)

func (c *Catalog) evictOldestLocked(keep string, filter poolFilter) bool {
	var oldestKey string
	var oldest time.Time
	for key, rec := range c.records {
		if key == keep || rec.building {
			continue
		}
		if filter.match && rec.scoped != filter.scoped {
			continue
		}
		if oldestKey == "" || rec.lastUsed.Before(oldest) {
			oldestKey, oldest = key, rec.lastUsed
		}
	}
	if oldestKey == "" {
		return false
	}
	delete(c.records, oldestKey)
	return true
}

func (c *Catalog) retainedBytesLocked() int64 {
	var total int64
	for _, rec := range c.records {
		total += rec.bytes
	}
	return total
}

// retainedBytes approximates what one published generation holds in memory.
func (s Snapshot) retainedBytes() int64 {
	var total int64
	for _, entry := range s.Entries {
		total += int64(len(entry.RootID)+len(entry.Path)+len(entry.Parent)+len(entry.Name)) + entryOverheadBytes
	}
	return total
}

func scopedRoots(roots []Root) bool {
	for _, root := range roots {
		if !root.Scoped() {
			return false
		}
	}
	return len(roots) > 0
}

func treeStoreEvictableLocked(store projectionStore) bool {
	if store.core().building {
		return false
	}
	if index, ok := store.(*indexStore); ok {
		if index.holders > 0 || index.navigation.users > 0 || len(index.pins.held) > 0 || index.checkpoint.done != nil || index.projections > 0 {
			return false
		}
	}
	for _, build := range store.contentBuilds() {
		select {
		case <-build.done:
		default:
			return false
		}
	}
	return true
}

// Admission can change after candidate selection, so retirement rechecks under the same lock.
func retireTreeStore(store projectionStore) bool {
	core := store.core()
	core.mu.Lock()
	defer core.mu.Unlock()
	if !treeStoreEvictableLocked(store) {
		return false
	}
	core.retired = true
	if index, ok := store.(*indexStore); ok {
		if index.inventory.cancel != nil {
			index.inventory.cancel()
		}
		_ = index.observations.CancelAll()
		_ = index.drainStructuralCheckpointLocked()
		index.pins.drained = true
		index.releaseCompletedStructureLocked()
		index.navigation.retired = true
		index.closeNavigationLocked()
		if index.structure != nil {
			index.structure.close()
			index.structure = nil
		}
	}
	return true
}

// HoldRoot keeps a root's index store resident for an open view. Between reads
// a view pins nothing, so without a hold the store-count bound could retire the
// store under it; explicit root release and drain still retire a held store.
// The returned release is idempotent.
func (c *Catalog) HoldRoot(ctx context.Context, projectID string, root Root) (func(), error) {
	var retired *indexStore
	for {
		store, err := c.indexStore(ctx, projectID, root)
		if err != nil {
			return nil, err
		}
		store.mu.Lock()
		if store.retired {
			store.mu.Unlock()
			// Eviction removes what it retires, so the next lookup opens a
			// successor; a drained catalog keeps answering the retired store.
			if store == retired {
				return nil, pagedview.ErrExpired
			}
			retired = store
			continue
		}
		store.holders++
		store.mu.Unlock()
		var once sync.Once
		return func() {
			once.Do(func() {
				store.mu.Lock()
				store.holders--
				store.mu.Unlock()
			})
		}, nil
	}
}
