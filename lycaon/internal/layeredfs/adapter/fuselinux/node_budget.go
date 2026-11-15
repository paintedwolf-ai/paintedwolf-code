//go:build linux

package fuselinux

import (
	"container/list"
	"path"
)

// DefaultNodeBudget is how many nodes the kernel may hold before the server asks it to drop the
// least recently used ones. Each node costs the engine memory until the kernel forgets it.
const DefaultNodeBudget = 131072

// evictionBatch is how many dentries one pass asks the kernel to drop.
const evictionBatch = 1024

// entryRef names a dentry by its parent node and name, the form an invalidation takes.
type entryRef struct {
	parent uint64
	name   string
}

// touchLocked marks fn as just used. Only nodes the kernel holds and no descriptor keeps open are
// candidates for eviction.
func (t *nodeTable) touchLocked(fn *fuseNode) {
	if fn.fuseID == fuseRootID {
		return
	}
	if fn.lruElem != nil {
		t.lru.MoveToFront(fn.lruElem)
		return
	}
	fn.lruElem = t.lru.PushFront(fn)
}

func (t *nodeTable) untrackLocked(fn *fuseNode) {
	if fn.lruElem != nil {
		t.lru.Remove(fn.lruElem)
		fn.lruElem = nil
	}
}

// overBudget reports whether the kernel holds more nodes than the budget allows.
func (t *nodeTable) overBudget() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.budget > 0 && len(t.byFuseID) > t.budget
}

// evictionCandidates names the dentries of the least recently used nodes beyond the budget. The
// kernel drops each dentry it can and forgets the node once nothing refers to it; a node in use
// stays, and the next pass reaches further.
func (t *nodeTable) evictionCandidates() []entryRef {
	t.mu.RLock()
	defer t.mu.RUnlock()
	excess := len(t.byFuseID) - t.budget
	if t.budget <= 0 || excess <= 0 {
		return nil
	}
	limit := min(excess, evictionBatch)
	out := make([]entryRef, 0, limit)
	for e := t.lru.Back(); e != nil && len(out) < limit; e = e.Prev() {
		fn := e.Value.(*fuseNode)
		if fn.openCount != 0 || fn.nlookup == 0 {
			continue
		}
		for p := range fn.paths {
			parent := t.byRel[cleanFuseRel(path.Dir(p))]
			if parent != nil {
				out = append(out, entryRef{parent: parent.fuseID, name: path.Base(p)})
			}
		}
	}
	return out
}

// evictLoop runs apart from request handling: an invalidation waits on the directory's lock, which
// a request in flight may hold until its reply is written.
func (s *Server) evictLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopCh:
			return
		case <-s.evictWake:
		}
		for _, ref := range s.nodes.evictionCandidates() {
			s.invalidateEntry(ref.parent, ref.name)
		}
	}
}

// wakeEvictor asks for an eviction pass without blocking the request that grew the table.
func (s *Server) wakeEvictor() {
	if !s.nodes.overBudget() {
		return
	}
	select {
	case s.evictWake <- struct{}{}:
	default:
	}
}

func newNodeLRU() *list.List { return list.New() }
