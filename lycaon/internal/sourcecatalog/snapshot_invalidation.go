package sourcecatalog

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
)

// InvalidateProject marks every generation for projectID stale.
func (c *Catalog) InvalidateProject(projectID string) {
	if c == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	roots := make(map[string]struct{})
	c.Trees.mu.Lock()
	for _, s := range c.Trees.trees {
		if core := s.core(); core.projectID == projectID {
			roots[core.root.Path] = struct{}{}
		}
	}
	c.Trees.mu.Unlock()
	c.mu.Lock()
	for _, rec := range c.records {
		if rec.projectID == projectID {
			rec.stale = true
			rec.mustAdvanceRevision = true
			rec.fullReconcile = true
			for _, root := range rec.roots {
				roots[root.Path] = struct{}{}
			}
		}
	}
	c.mu.Unlock()
	for root := range roots {
		c.Trees.invalidateTrees(root, nil)
		c.Literals.cache.invalidate(root, nil)
	}
}

// InvalidateRoot marks generations containing rootPath stale.
func (c *Catalog) InvalidateRoot(rootPath string, paths ...string) {
	c.InvalidateRootChange(rootPath, paths, repochange.StructuralPathSet{Paths: paths, Full: len(paths) == 0})
}

func (c *Catalog) InvalidateRootChange(rootPath string, contentPaths []string, structural repochange.StructuralPathSet) {
	rootPath = c.invalidateRootContent(rootPath, contentPaths)
	if rootPath == "" {
		return
	}
	c.invalidateRootStructure(rootPath, structural)
	c.observeRecords(rootPath)
}

// observeEpoch classifies an epoch advance that left rootPath's tree unchanged.
func (c *Catalog) observeEpoch(rootPath string) {
	rootPath = cleanAbs(rootPath)
	if c == nil || rootPath == "" {
		return
	}
	c.observeRecords(rootPath)
	c.Trees.observeTreeEpoch(rootPath)
}

// observeRecords marks rootPath's current epoch classified. A generation with
// nothing pending adopts it, so a change that left the tree alone costs no walk.
func (c *Catalog) observeRecords(rootPath string) {
	epoch := repochange.CurrentEpoch(rootPath)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rec := range c.records {
		for _, root := range rec.roots {
			if cleanAbs(root.Path) != rootPath {
				continue
			}
			rec.observed = epoch
			if _, pinned := rec.snapshot.Epochs[root.ID]; pinned && !rec.building && !rec.stale {
				rec.snapshot = rec.snapshot.pinnedAt(root.ID, epoch)
			}
			break
		}
	}
}

func (c *Catalog) invalidateRootContent(rootPath string, paths []string) string {
	rootPath = c.markRootStale(rootPath, true, paths...)
	if rootPath != "" {
		c.Literals.cache.invalidate(rootPath, paths)
	}
	return rootPath
}

func (c *Catalog) invalidateRootStructure(rootPath string, structural repochange.StructuralPathSet) {
	if structural.Full {
		c.Trees.invalidateTrees(rootPath, nil)
		return
	}
	if len(structural.Paths) > 0 {
		c.Trees.invalidateTrees(rootPath, structural.Paths)
		return
	}
	c.Trees.observeTreeEpoch(rootPath)
}

// requestReconcile asks for root's next generation. A full request re-walks
// the tree; otherwise the pending changes decide what is reconciled.
func (c *Catalog) requestReconcile(projectID string, root Root, full bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rec := c.records[rootKey(projectID, root)]
	if rec == nil {
		return
	}
	rec.stale = true
	if full {
		rec.fullReconcile = true
		rec.dirtyPaths = nil
	}
}

func (c *Catalog) markRootStale(rootPath string, advanceRevision bool, paths ...string) string {
	if c == nil {
		return ""
	}
	rootPath = cleanAbs(rootPath)
	if rootPath == "" {
		return ""
	}
	c.mu.Lock()
	for _, rec := range c.records {
		for _, root := range rec.roots {
			// A subtree generation hears its attached root's changes below it.
			changed, relevant := treeChangesForRoot(rootPath, root.Path, paths)
			if !relevant {
				continue
			}
			if len(changed) == 0 {
				rec.stale = true
				rec.fullReconcile = true
				rec.dirtyPaths = nil
			} else if !rec.fullReconcile {
				// Unobserved descendants do not invalidate this generation. A walk
				// in flight keeps every path: its boundaries are not known yet.
				observed := changed
				if !rec.building {
					observed = rec.snapshot.observedChanges(root, changed)
				}
				if len(observed) == 0 {
					break
				}
				rec.stale = true
				if rec.dirtyPaths == nil {
					rec.dirtyPaths = map[string]struct{}{}
				}
				for _, changed := range observed {
					if len(rec.dirtyPaths) >= maxPendingTreePaths {
						rec.fullReconcile = true
						rec.dirtyPaths = nil
						break
					}
					rec.dirtyPaths[changed] = struct{}{}
				}
			} else {
				rec.stale = true
			}
			if advanceRevision {
				rec.mustAdvanceRevision = true
			}
			break
		}
	}
	c.mu.Unlock()
	return rootPath
}

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
		if index.holders > 0 || index.navigation.Active() || len(index.pins.held) > 0 || index.checkpoint.Active() || index.projections > 0 {
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
		_ = index.checkpoint.Drain()
		index.pins.drained = true
		index.releaseCompletedStructureLocked()
		_ = index.navigation.Drain()
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
func (c *TreeStores) HoldRoot(ctx context.Context, projectID string, root Root) (func(), error) {
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
