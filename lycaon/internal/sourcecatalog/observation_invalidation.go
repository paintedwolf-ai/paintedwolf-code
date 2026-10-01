package sourcecatalog

import (
	"errors"
	"path"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
)

var observationSerial atomic.Uint64

var errObservationChanged = errors.New("directory changed during discovery")

// Bounded invalidations persist with observations; overflow coarsens onto
// shallower subtrees, and only an exhausted set marks the whole root.
type observationInvalidation struct {
	next, full uint64
	paths      map[string]uint64
	parents    map[string]uint64
}

func (s *indexStore) invalidateObservationsLocked(paths []string) {
	if len(paths) > 0 {
		normalized, valid := reconciliationPaths(s.root, paths)
		paths = nil
		if valid {
			for _, rel := range normalized {
				if validateObservationDirectory(s.root, rel) == nil {
					paths = append(paths, rel)
				}
			}
			if len(paths) == 0 {
				return
			}
		}
	}
	s.inventory.invalidate(paths)
	marks := &s.invalidation
	marks.next = observationSerial.Add(1)
	if len(paths) == 0 {
		marks.full = marks.next
		marks.paths = nil
		marks.parents = nil
		return
	}
	if marks.paths == nil {
		marks.paths = make(map[string]uint64)
		marks.parents = make(map[string]uint64)
	}
	for _, rel := range paths {
		marks.paths[rel] = marks.next
		marks.parents[normalizeDir(path.Dir(rel))] = marks.next
	}
	if len(marks.paths)+len(marks.parents) > maxPendingTreePaths {
		marks.coarsenLocked(maxPendingTreePaths)
	}
}

// invalidateListingsLocked marks the listings of dirs stale without touching
// their descendants, and queues each for the next inventory pass.
func (s *indexStore) invalidateListingsLocked(dirs []string) {
	if len(dirs) == 0 {
		return
	}
	marks := &s.invalidation
	marks.next = observationSerial.Add(1)
	if marks.paths == nil {
		marks.paths = make(map[string]uint64)
		marks.parents = make(map[string]uint64)
	}
	if s.inventory.dirty == nil {
		s.inventory.dirty = make(map[string]struct{})
	}
	for _, dir := range dirs {
		marks.parents[dir] = marks.next
		s.inventory.dirty[dir] = struct{}{}
	}
	if len(marks.paths)+len(marks.parents) > maxPendingTreePaths {
		marks.coarsenLocked(maxPendingTreePaths)
	}
	if len(s.inventory.dirty) > maxPendingTreePaths {
		coarsened, escalated := coarsenDirtyDirectories(s.inventory.dirty, maxPendingTreePaths)
		s.inventory.dirty, s.inventory.full = coarsened, s.inventory.full || escalated
	}
	select {
	case s.inventory.wake <- struct{}{}:
	default:
	}
}

// coarsenDirtyDirectories lifts an overflowing dirty set onto shallower
// ancestors instead of the root. Entries already under the root stay put, so
// only a set broad at every depth becomes a whole-tree pass.
func coarsenDirtyDirectories(dirty map[string]struct{}, limit int) (map[string]struct{}, bool) {
	for len(dirty) > limit {
		lifted := make(map[string]struct{}, len(dirty))
		for dir := range dirty {
			if dir == "." {
				return map[string]struct{}{".": {}}, true
			}
			if parent := path.Dir(dir); parent != "." {
				dir = parent
			}
			lifted[dir] = struct{}{}
		}
		if len(lifted) >= len(dirty) {
			return map[string]struct{}{".": {}}, true
		}
		dirty = lifted
	}
	return dirty, false
}

// coarsenLocked keeps overflowing invalidation from collapsing into a whole-root
// mark. A path mark already covers everything beneath it, so lifting marks onto
// their parents keeps coverage with fewer entries.
func (m *observationInvalidation) coarsenLocked(limit int) {
	for len(m.paths)+len(m.parents) > limit {
		total := len(m.paths) + len(m.parents)
		lifted := make(map[string]uint64, total)
		// The root listing has no shallower mark to lift onto, so it is carried.
		parents := make(map[string]uint64, 1)
		rooted := false
		for dir, mark := range m.paths {
			if dir == "." {
				rooted = true
				break
			}
			if parent := path.Dir(dir); parent != "." {
				dir = parent
			}
			lifted[dir] = max(lifted[dir], mark)
		}
		for dir, mark := range m.parents {
			if dir == "." {
				parents["."] = max(parents["."], mark)
				continue
			}
			// A listing mark becomes a subtree mark at the same depth: it covers
			// strictly more, and lifts with everything else next round.
			lifted[dir] = max(lifted[dir], mark)
		}
		if rooted || len(lifted)+len(parents) >= total {
			m.full = m.next
			m.paths, m.parents = nil, nil
			return
		}
		m.paths, m.parents = lifted, parents
	}
}

func (s *indexStore) observationMark(dir string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observationMarkLocked(dir)
}

func (s *indexStore) observationMarkLocked(dir string) uint64 {
	mark := max(s.invalidation.full, s.invalidation.parents[dir])
	for current := dir; ; current = path.Dir(current) {
		mark = max(mark, s.invalidation.paths[current])
		if current == "." {
			return mark
		}
	}
}

func (s *indexStore) observationFresh(observation DirectoryObservation) bool {
	if !observation.Complete || observation.Failure != "" || observation.Invalidation < s.observationMark(observation.Path) {
		return false
	}
	epoch := repochange.CurrentEpoch(s.root.Path)
	if epoch.BootID != observation.Epoch.BootID {
		return false
	}
	if repochange.DirWatched(s.root.Path, observation.Path) {
		return true
	}
	return repochange.EpochCurrent(s.root.Path, observation.Epoch) && time.Since(observation.Observed) < 2*time.Second
}
