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
	// adoptedInos keeps the inode number a file had before its first copy-up, so it survives
	// eviction of the node. One entry per copied-up object, the same order as Core's private nodes.
	adoptedInos map[layeredfs.NodeID]uint64
}

// privateInoBase separates Core's small private node IDs from the root inode and from its
// synthetic IDs for lower-layer entries, which have bit 62 set.
const privateInoBase = uint64(1) << 61

func newNodeTable() *nodeTable {
	rootNode := &fuseNode{
		fuseID:     fuseRootID,
		generation: 1,
		ino:        fuseRootID,
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
		adoptedInos:     make(map[layeredfs.NodeID]uint64),
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

// inoLocked returns the st_ino for a Core identity. Node IDs are per kernel reference and change
// across eviction, so st_ino comes from Core: tools treat a changed inode as a replaced file.
func (t *nodeTable) inoLocked(id layeredfs.NodeID, layer layeredfs.LayerSource) uint64 {
	if layer != layeredfs.LayerPrivate {
		return uint64(id)
	}
	if ino, ok := t.adoptedInos[id]; ok {
		return ino
	}
	return privateInoBase | uint64(id)
}

func (t *nodeTable) nodeIno(nodeID uint64) (uint64, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	fn, ok := t.byFuseID[nodeID]
	if !ok {
		return 0, false
	}
	return fn.ino, true
}

func (t *nodeTable) direntIno(e layeredfs.DirEntry) uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.inoLocked(e.NodeID, e.Layer)
}

// adoptLocked binds a lower-layer node to the private object Core created for it, keeping its identity.
func (t *nodeTable) adoptLocked(fn *fuseNode, attr layeredfs.EntryAttr) {
	if attr.Layer != layeredfs.LayerPrivate || attr.NodeID == 0 || fn.corePrivID != 0 || fn.fuseID == fuseRootID {
		return
	}
	if _, taken := t.byCorePrivateID[attr.NodeID]; taken {
		return
	}
	fn.corePrivID, fn.corePrivGen = attr.NodeID, attr.Generation
	t.byCorePrivateID[attr.NodeID] = fn
	t.adoptedInos[attr.NodeID] = fn.ino
}

// privateIdentity returns the Core identity of a node that has one.
func (t *nodeTable) privateIdentity(nodeID uint64) (layeredfs.NodeID, layeredfs.Generation, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	fn, ok := t.byFuseID[nodeID]
	if !ok || fn.corePrivID == 0 {
		return 0, 0, false
	}
	return fn.corePrivID, fn.corePrivGen, true
}

// acquireNode resolves the node for a reply that gives the kernel an entry, and counts that
// reference. The count must rise before the reply is written, or a FORGET could evict the node
// while the kernel still holds it. IDs are never reused, so generation stays constant.
func (t *nodeTable) acquireNode(rel string, attr layeredfs.EntryAttr) *fuseNode {
	clean := cleanFuseRel(rel)
	t.mu.Lock()
	defer t.mu.Unlock()
	fn := t.resolveForRefLocked(clean, attr)
	fn.nlookup++
	return fn
}

func (t *nodeTable) resolveForRefLocked(clean string, attr layeredfs.EntryAttr) *fuseNode {
	if clean == "." {
		return t.byFuseID[fuseRootID]
	}
	if attr.Layer == layeredfs.LayerPrivate && attr.NodeID != 0 {
		if fn, ok := t.byCorePrivateID[attr.NodeID]; ok {
			t.displaceRelLocked(clean, fn)
			fn.paths[clean] = struct{}{}
			fn.unlinked = false
			t.byRel[clean] = fn
			return fn
		}
		if existing, ok := t.byRel[clean]; ok && existing.corePrivID == 0 {
			t.adoptLocked(existing, attr)
			existing.unlinked = false
			return existing
		}
	} else if fn, ok := t.byRel[clean]; ok {
		fn.unlinked = false
		return fn
	}
	fn := &fuseNode{
		fuseID:     t.nextFuseID,
		generation: 1,
		ino:        t.inoLocked(attr.NodeID, attr.Layer),
		paths:      map[string]struct{}{clean: {}},
	}
	t.nextFuseID++
	t.displaceRelLocked(clean, fn)
	t.byFuseID[fn.fuseID] = fn
	t.byRel[clean] = fn
	if attr.Layer == layeredfs.LayerPrivate && attr.NodeID != 0 {
		fn.corePrivID, fn.corePrivGen = attr.NodeID, attr.Generation
		t.byCorePrivateID[attr.NodeID] = fn
	}
	return fn
}

// refreshNode records what a reply without a kernel reference learned about an existing node and
// returns its st_ino. It never creates a node: only referenced nodes may exist.
func (t *nodeTable) refreshNode(nodeID uint64, attr layeredfs.EntryAttr) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	fn, ok := t.byFuseID[nodeID]
	if !ok {
		return t.inoLocked(attr.NodeID, attr.Layer)
	}
	t.adoptLocked(fn, attr)
	return fn.ino
}

// forget applies the kernel's dropped references. A node still holding a descriptor stays until
// the descriptor's release, which the kernel may deliver after FORGET.
func (t *nodeTable) forget(nodeID, count uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fn, ok := t.byFuseID[nodeID]
	if !ok || nodeID == fuseRootID {
		return
	}
	if count >= fn.nlookup {
		fn.nlookup = 0
	} else {
		fn.nlookup -= count
	}
	t.evictIfUnreferencedLocked(fn)
}

func (t *nodeTable) evictIfUnreferencedLocked(fn *fuseNode) {
	if fn.nlookup != 0 || fn.openCount != 0 || fn.fuseID == fuseRootID {
		return
	}
	for p := range fn.paths {
		if t.byRel[p] == fn {
			delete(t.byRel, p)
		}
	}
	if fn.corePrivID != 0 && t.byCorePrivateID[fn.corePrivID] == fn {
		delete(t.byCorePrivateID, fn.corePrivID)
	}
	delete(t.byFuseID, fn.fuseID)
}

// settleDirtyRels forgets recorded paths once a flush covered them. Later flushes still reach
// every private change through Core's change list, and every writable descriptor directly.
func (t *nodeTable) settleDirtyRels() {
	t.mu.Lock()
	t.dirtyRels = make(map[string]struct{})
	t.mu.Unlock()
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
	if fn, ok := t.byFuseID[entry.fuseID]; ok {
		fn.openCount++
	}
	if entry.writable && entry.rel != "" {
		t.dirtyRels[entry.rel] = struct{}{}
	}
	t.mu.Unlock()
}

// takeHandle removes a released descriptor and any stream snapshot, then settles its node.
func (t *nodeTable) takeHandle(fh uint64) (openHandleEntry, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h, ok := t.openHandles[fh]
	delete(t.openHandles, fh)
	delete(t.dirSnapshots, fh)
	if !ok {
		return h, false
	}
	if fn, tracked := t.byFuseID[h.fuseID]; tracked {
		fn.openCount--
		t.evictIfUnreferencedLocked(fn)
	}
	return h, true
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
