package fuselinux

import (
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// nodeTable maps the kernel's node IDs and open descriptors to view paths. It holds identity
// only: file content and namespace state stay in Core.
type nodeTable struct {
	mu              sync.RWMutex
	nextFuseID      uint64
	byFuseID        map[uint64]*fuseNode
	byRel           map[string]*fuseNode
	byCorePrivateID map[layeredfs.NodeID]*fuseNode
	openHandles     map[uint64]openHandleEntry
	dirSnapshots    map[uint64][]rawDirent
	dirtyRels       map[string]struct{}
	nextDirFH       uint64
}

func newNodeTable() *nodeTable {
	rootNode := &fuseNode{
		fuseID:     fuseRootID,
		generation: 1,
		paths:      map[string]struct{}{".": {}},
	}
	return &nodeTable{
		nextFuseID:      2,
		byFuseID:        map[uint64]*fuseNode{fuseRootID: rootNode},
		byRel:           map[string]*fuseNode{".": rootNode},
		byCorePrivateID: make(map[layeredfs.NodeID]*fuseNode),
		openHandles:     make(map[uint64]openHandleEntry),
		dirSnapshots:    make(map[uint64][]rawDirent),
		dirtyRels:       make(map[string]struct{}),
		nextDirFH:       1 << 50,
	}
}

func (t *nodeTable) recordDirtyRel(rel string) {
	clean := cleanFuseRel(rel)
	t.mu.Lock()
	t.dirtyRels[clean] = struct{}{}
	if clean != "." {
		if parent := path.Dir(clean); parent != "" {
			t.dirtyRels[cleanFuseRel(parent)] = struct{}{}
		}
	}
	t.mu.Unlock()
}

// dirtyCandidatePaths returns the sorted union of relative paths mutated or held writable via FUSE.
func (t *nodeTable) dirtyCandidatePaths() []string {
	t.mu.RLock()
	seen := make(map[string]struct{}, len(t.dirtyRels)+len(t.openHandles))
	for r := range t.dirtyRels {
		seen[r] = struct{}{}
	}
	for _, h := range t.openHandles {
		if h.writable && h.rel != "" {
			seen[cleanFuseRel(h.rel)] = struct{}{}
		}
	}
	t.mu.RUnlock()
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

func (t *nodeTable) resolveNodeRel(nodeID uint64) (string, bool) {
	if nodeID == fuseRootID {
		return ".", true
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if fn, ok := t.byFuseID[nodeID]; ok {
		return fn.primaryRel()
	}
	return "", false
}

func (t *nodeTable) ensureNode(rel string, attr layeredfs.EntryAttr) *fuseNode {
	clean := cleanFuseRel(rel)
	if clean == "." {
		t.mu.RLock()
		defer t.mu.RUnlock()
		return t.byFuseID[fuseRootID]
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if attr.Layer == layeredfs.LayerPrivate && attr.NodeID != 0 {
		if fn, ok := t.byCorePrivateID[attr.NodeID]; ok {
			t.displaceRelLocked(clean, fn)
			fn.paths[clean] = struct{}{}
			fn.unlinked = false
			t.byRel[clean] = fn
			return fn
		}
		if existing, ok := t.byRel[clean]; ok && existing.corePrivID == 0 {
			existing.corePrivID = attr.NodeID
			existing.paths[clean] = struct{}{}
			existing.unlinked = false
			t.byCorePrivateID[attr.NodeID] = existing
			return existing
		}
		id := t.nextFuseID
		t.nextFuseID++
		fn := &fuseNode{
			fuseID:     id,
			generation: 1,
			corePrivID: attr.NodeID,
			paths:      map[string]struct{}{clean: {}},
		}
		t.byFuseID[id] = fn
		t.byCorePrivateID[attr.NodeID] = fn
		t.byRel[clean] = fn
		return fn
	}
	if fn, ok := t.byRel[clean]; ok {
		fn.unlinked = false
		return fn
	}
	id := t.nextFuseID
	t.nextFuseID++
	fn := &fuseNode{
		fuseID:     id,
		generation: 1,
		paths:      map[string]struct{}{clean: {}},
	}
	t.byFuseID[id] = fn
	t.byRel[clean] = fn
	return fn
}

// displaceRelLocked detaches rel from any other node it named. Otherwise a descriptor on the
// replaced object would resolve to, and could mutate, the object now at that path.
func (t *nodeTable) displaceRelLocked(rel string, next *fuseNode) {
	prev, ok := t.byRel[rel]
	if !ok || prev == next {
		return
	}
	delete(prev.paths, rel)
	if len(prev.paths) == 0 {
		prev.unlinked = true
	}
}

func (t *nodeTable) recordUnlink(rel string, attr layeredfs.EntryAttr, hadPriorAttr bool) {
	clean := cleanFuseRel(rel)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dirtyRels[clean] = struct{}{}
	fn, ok := t.byRel[clean]
	if !ok && hadPriorAttr && attr.Layer == layeredfs.LayerPrivate && attr.NodeID != 0 {
		fn = t.byCorePrivateID[attr.NodeID]
	}
	delete(t.byRel, clean)
	if fn != nil {
		delete(fn.paths, clean)
		if len(fn.paths) == 0 {
			fn.unlinked = true
		}
	}
}

func (t *nodeTable) recordRename(oldRel, newRel string) {
	oldClean := cleanFuseRel(oldRel)
	newClean := cleanFuseRel(newRel)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dirtyRels[oldClean] = struct{}{}
	t.dirtyRels[newClean] = struct{}{}
	if source := t.byRel[oldClean]; t.byRel[newClean] != source {
		t.displaceRelLocked(newClean, source)
		delete(t.byRel, newClean)
	}
	prefix := oldClean + "/"
	var moves [][2]string
	for rel := range t.byRel {
		if rel == oldClean {
			moves = append(moves, [2]string{oldClean, newClean})
		} else if strings.HasPrefix(rel, prefix) {
			moves = append(moves, [2]string{rel, newClean + strings.TrimPrefix(rel, oldClean)})
		}
	}
	for _, mv := range moves {
		fn := t.byRel[mv[0]]
		delete(t.byRel, mv[0])
		if fn != nil {
			delete(fn.paths, mv[0])
			fn.paths[mv[1]] = struct{}{}
			t.byRel[mv[1]] = fn
		}
	}
	for fh, h := range t.openHandles {
		if h.rel == oldClean {
			h.rel = newClean
			t.openHandles[fh] = h
		} else if strings.HasPrefix(h.rel, prefix) {
			h.rel = newClean + strings.TrimPrefix(h.rel, oldClean)
			t.openHandles[fh] = h
		}
	}
}

func (t *nodeTable) storeOpenHandle(fh uint64, entry openHandleEntry) {
	t.mu.Lock()
	t.openHandles[fh] = entry
	if entry.writable && entry.rel != "" {
		t.dirtyRels[entry.rel] = struct{}{}
	}
	t.mu.Unlock()
}

func (t *nodeTable) lookupHandle(fh uint64) (openHandleEntry, bool) {
	t.mu.RLock()
	h, ok := t.openHandles[fh]
	t.mu.RUnlock()
	return h, ok
}

func (t *nodeTable) lookupOpenHandleForNode(fh, nodeID uint64) (openHandleEntry, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if fh != 0 {
		if h, ok := t.openHandles[fh]; ok {
			return h, true
		}
	}
	for _, h := range t.openHandles {
		if h.fuseID == nodeID && !h.isDir && h.hid != 0 {
			return h, true
		}
	}
	return openHandleEntry{}, false
}
