package sourcecatalog

import (
	"strings"

	"github.com/lycaon/lycaon/internal/repochange"
)

// InvalidateProject marks every generation for projectID stale.
func (c *Catalog) InvalidateProject(projectID string) {
	if c == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	roots := make(map[string]struct{})
	c.mu.Lock()
	for _, s := range c.trees {
		if core := s.core(); core.projectID == projectID {
			roots[core.root.Path] = struct{}{}
		}
	}
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
		c.invalidateTrees(root, nil)
		c.literals.invalidate(root, nil)
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
	c.observeTreeEpoch(rootPath)
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
		c.literals.invalidate(rootPath, paths)
	}
	return rootPath
}

func (c *Catalog) invalidateRootStructure(rootPath string, structural repochange.StructuralPathSet) {
	if structural.Full {
		c.invalidateTrees(rootPath, nil)
		return
	}
	if len(structural.Paths) > 0 {
		c.invalidateTrees(rootPath, structural.Paths)
		return
	}
	c.observeTreeEpoch(rootPath)
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
