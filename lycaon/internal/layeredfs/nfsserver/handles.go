package nfsserver

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

const (
	// maxUnlinkedHeld bounds objects kept readable after REMOVE. The kernel client renames an open
	// file instead of removing it, so this serves only clients that really remove while using it.
	maxUnlinkedHeld = 64
)

type handleKey struct {
	viewHash uint64
	nodeID   layeredfs.NodeID
}
type pathKey struct {
	viewID  ViewID
	root    RootID
	relPath string
}
type wireHeader struct {
	root                 RootID
	viewHash, generation uint64
	nodeID, parentID     layeredfs.NodeID
}
type unlinkedFile struct {
	fd   layeredfs.HandleID
	meta layeredfs.EntryAttr
	elem *list.Element
}
type handleRecord struct {
	wire       [HandleSize]byte
	key        handleKey
	viewID     ViewID
	qpath      layeredfs.QualifiedPath
	paths      map[pathKey]layeredfs.QualifiedPath
	generation uint64
	isRoot     bool
	retained   bool
	createVerf *[8]byte
	unlinked   *unlinkedFile
	lruElem    *list.Element
}

// handleTable holds issued handles. NFSv3 has no FORGET, so the server never learns the client
// dropped one and must keep every issued handle answerable: directories stay resident, and any
// other record may leave memory because Resolve rebuilds it from identity carried in the handle.
// Memory is therefore bounded by maxHandles plus the directories the client has visited.
type handleTable struct {
	mu          sync.RWMutex
	secret      [32]byte
	maxHandles  int
	activeViews map[uint64]ViewID
	byKey       map[handleKey]*handleRecord
	byPath      map[pathKey]*handleRecord
	lru         *list.List
	unlinked    *list.List
	coreFor     func(ViewID) *layeredfs.Core
	sidecars    *sidecarTable
}

func newHandleTable(secret [32]byte, maxHandles int, sidecars *sidecarTable, coreFor func(ViewID) *layeredfs.Core) *handleTable {
	if maxHandles <= 0 {
		maxHandles = DefaultMaxHandles
	}
	return &handleTable{
		secret: secret, maxHandles: maxHandles,
		activeViews: make(map[uint64]ViewID),
		byKey:       make(map[handleKey]*handleRecord),
		byPath:      make(map[pathKey]*handleRecord),
		lru:         list.New(),
		unlinked:    list.New(),
		coreFor:     coreFor,
		sidecars:    sidecars,
	}
}

func computeViewHash(viewID ViewID) uint64 {
	sum := sha256.Sum256([]byte(viewID))
	if v := binary.BigEndian.Uint64(sum[:8]); v != 0 {
		return v
	}
	return 1
}

func (t *handleTable) RegisterView(viewID ViewID) {
	t.mu.Lock()
	t.activeViews[computeViewHash(viewID)] = viewID
	t.mu.Unlock()
}

func (t *handleTable) ReleaseView(core *layeredfs.Core, viewID ViewID) {
	t.mu.Lock()
	vHash := computeViewHash(viewID)
	delete(t.activeViews, vHash)
	var toClose []layeredfs.HandleID
	for k, rec := range t.byKey {
		if k.viewHash != vHash {
			continue
		}
		toClose = append(toClose, t.takeUnlinkedLocked(rec)...)
		for pk := range rec.paths {
			delete(t.byPath, pk)
		}
		if rec.lruElem != nil {
			t.lru.Remove(rec.lruElem)
			rec.lruElem = nil
		}
		delete(t.byKey, k)
	}
	t.mu.Unlock()
	closeCoreHandles(core, toClose)
}

func closeCoreHandles(core *layeredfs.Core, fds []layeredfs.HandleID) {
	if core == nil {
		return
	}
	for _, fd := range fds {
		_ = core.CloseHandle(fd)
	}
}

func (t *handleTable) takeUnlinkedLocked(rec *handleRecord) []layeredfs.HandleID {
	if rec.unlinked == nil {
		return nil
	}
	var out []layeredfs.HandleID
	if rec.unlinked.fd != 0 {
		out = append(out, rec.unlinked.fd)
		rec.unlinked.fd = 0
	}
	if rec.unlinked.elem != nil {
		t.unlinked.Remove(rec.unlinked.elem)
		rec.unlinked.elem = nil
	}
	return out
}

func cleanRel(rel string) string {
	if rel == "" || rel == "." {
		return "."
	}
	return rel
}

func makePathKey(viewID ViewID, qpath layeredfs.QualifiedPath) pathKey {
	return pathKey{viewID: viewID, root: qpath.RootID, relPath: cleanRel(qpath.Rel)}
}

// Ensure returns the handle for the object now at qpath. The wire form never changes once issued,
// including across copy-up, because the identity it carries is the stable one.
func (t *handleTable) Ensure(
	viewID ViewID, qpath layeredfs.QualifiedPath, meta layeredfs.EntryAttr, parentID layeredfs.NodeID, isRoot bool,
) [HandleSize]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	qpath.Rel = cleanRel(qpath.Rel)
	vHash := computeViewHash(viewID)
	t.activeViews[vHash] = viewID
	hk := handleKey{viewHash: vHash, nodeID: meta.StableID()}
	wire := encodeWireHandle(t.secret, vHash, qpath.RootID, hk.nodeID, parentID, uint64(meta.Generation))
	return t.insertLocked(hk, viewID, qpath, meta, isRoot, wire).wire
}

func (t *handleTable) insertLocked(
	hk handleKey, viewID ViewID, qpath layeredfs.QualifiedPath, meta layeredfs.EntryAttr, isRoot bool, wire [HandleSize]byte,
) *handleRecord {
	pk := makePathKey(viewID, qpath)
	existing := t.byKey[hk]
	if occupant, ok := t.byPath[pk]; ok && occupant != existing {
		// The name now holds a different object; the old record keeps only its other names.
		t.dropRecordPathLocked(occupant, pk, qpath)
	}
	if existing != nil {
		existing.paths[pk] = qpath
		existing.qpath = qpath
		existing.isRoot = existing.isRoot || isRoot
		t.byPath[pk] = existing
		if existing.lruElem != nil {
			t.lru.MoveToFront(existing.lruElem)
		}
		return existing
	}
	rec := &handleRecord{
		wire: wire, key: hk, viewID: viewID, qpath: qpath,
		paths:      map[pathKey]layeredfs.QualifiedPath{pk: qpath},
		generation: uint64(meta.Generation), isRoot: isRoot,
		retained: isRoot || meta.Kind == layeredfs.EntryDirectory,
	}
	if !rec.retained {
		t.evictLocked()
		rec.lruElem = t.lru.PushFront(rec)
	}
	t.byKey[hk], t.byPath[pk] = rec, rec
	return rec
}

func (t *handleTable) evictLocked() {
	for t.lru.Len() >= t.maxHandles {
		back := t.lru.Back()
		rec := back.Value.(*handleRecord)
		t.lru.Remove(back)
		rec.lruElem = nil
		delete(t.byKey, rec.key)
		for pk := range rec.paths {
			delete(t.byPath, pk)
		}
	}
}

// Resolve authenticates raw and returns its record, rebuilding one that left memory. A handle whose
// object no longer exists under its identity is stale; a client recovers by looking the name up again.
func (t *handleTable) Resolve(raw []byte, expectedViewHash uint64) (*handleRecord, error) {
	hdr, err := decodeWireHandle(t.secret, raw)
	if err != nil {
		return nil, err
	}
	if expectedViewHash != 0 && hdr.viewHash != expectedViewHash {
		return nil, ErrCrossViewHandle
	}
	t.mu.Lock()
	viewID, active := t.activeViews[hdr.viewHash]
	if !active {
		t.mu.Unlock()
		return nil, ErrStaleHandle
	}
	hk := handleKey{viewHash: hdr.viewHash, nodeID: hdr.nodeID}
	if rec, ok := t.byKey[hk]; ok {
		if rec.lruElem != nil {
			t.lru.MoveToFront(rec.lruElem)
		}
		clone := *rec
		t.mu.Unlock()
		return &clone, nil
	}
	parent, hasParent := t.byKey[handleKey{viewHash: hdr.viewHash, nodeID: hdr.parentID}]
	var parentPath layeredfs.QualifiedPath
	if hasParent {
		parentPath = parent.qpath
	}
	t.mu.Unlock()

	qpath, meta, err := t.locate(viewID, hdr, parentPath, hasParent)
	if err != nil {
		return nil, err
	}
	var wire [HandleSize]byte
	copy(wire[:], raw)
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, stillActive := t.activeViews[hdr.viewHash]; !stillActive {
		return nil, ErrStaleHandle
	}
	clone := *t.insertLocked(hk, viewID, qpath, meta, false, wire)
	return &clone, nil
}

func (t *handleTable) locate(
	viewID ViewID, hdr wireHeader, parentPath layeredfs.QualifiedPath, hasParent bool,
) (layeredfs.QualifiedPath, layeredfs.EntryAttr, error) {
	core := t.coreFor(viewID)
	if core == nil {
		return layeredfs.QualifiedPath{}, layeredfs.EntryAttr{}, ErrStaleHandle
	}
	if qpath, meta, ok := core.ResolveIdentity(hdr.nodeID); ok {
		return qpath, meta, nil
	}
	if hasParent {
		name, found, err := core.FindChildByIdentity(parentPath, hdr.nodeID)
		if err == nil && found {
			return locatedChild(core, parentPath, name, hdr.nodeID)
		}
	}
	if qpath, meta, ok := t.sidecars.locate(core, hdr.nodeID, parentPath, hasParent); ok {
		return qpath, meta, nil
	}
	return layeredfs.QualifiedPath{}, layeredfs.EntryAttr{}, ErrStaleHandle
}

func locatedChild(core *layeredfs.Core, parentPath layeredfs.QualifiedPath, name string, id layeredfs.NodeID) (layeredfs.QualifiedPath, layeredfs.EntryAttr, error) {
	qpath, err := joinChildPath(parentPath, name)
	if err != nil {
		return layeredfs.QualifiedPath{}, layeredfs.EntryAttr{}, ErrStaleHandle
	}
	meta, err := core.LstatAttrs(qpath)
	if err != nil || meta.StableID() != id {
		return layeredfs.QualifiedPath{}, layeredfs.EntryAttr{}, ErrStaleHandle
	}
	return qpath, meta, nil
}

func (t *handleTable) LookupByPath(viewID ViewID, qpath layeredfs.QualifiedPath) (*handleRecord, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if rec, ok := t.byPath[makePathKey(viewID, qpath)]; ok {
		clone := *rec
		return &clone, true
	}
	return nil, false
}

func (t *handleTable) SetCreateVerifier(hk handleKey, verf [8]byte) {
	t.mu.Lock()
	if rec, ok := t.byKey[hk]; ok {
		rec.createVerf = &verf
	}
	t.mu.Unlock()
}

func (t *handleTable) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.byKey)
}

// MarkUnlinked keeps the object at qpath reachable through openFD, returning a descriptor that fell
// off the bound for the caller to close. A handle whose object is no longer held turns stale.
func (t *handleTable) MarkUnlinked(viewID ViewID, qpath layeredfs.QualifiedPath, openFD layeredfs.HandleID, meta layeredfs.EntryAttr) []layeredfs.HandleID {
	t.mu.Lock()
	defer t.mu.Unlock()
	pk := makePathKey(viewID, qpath)
	rec, ok := t.byPath[pk]
	if !ok {
		return []layeredfs.HandleID{openFD}
	}
	delete(t.byPath, pk)
	delete(rec.paths, pk)
	if rec.lruElem != nil {
		t.lru.Remove(rec.lruElem)
		rec.lruElem = nil
	}
	rec.unlinked = &unlinkedFile{fd: openFD, meta: meta}
	rec.unlinked.elem = t.unlinked.PushFront(rec)
	var toClose []layeredfs.HandleID
	for t.unlinked.Len() > maxUnlinkedHeld {
		oldest := t.unlinked.Back().Value.(*handleRecord)
		toClose = append(toClose, t.takeUnlinkedLocked(oldest)...)
		delete(t.byKey, oldest.key)
	}
	return toClose
}

func (t *handleTable) UpdateUnlinkedMeta(hk handleKey, meta layeredfs.EntryAttr) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if rec, ok := t.byKey[hk]; ok && rec.unlinked != nil {
		rec.unlinked.meta = meta
	}
}

func (t *handleTable) dropRecordPathLocked(rec *handleRecord, pk pathKey, qpath layeredfs.QualifiedPath) layeredfs.HandleID {
	delete(t.byPath, pk)
	delete(rec.paths, pk)
	if len(rec.paths) > 0 {
		if rec.qpath == qpath {
			for _, remQP := range rec.paths {
				rec.qpath = remQP
				break
			}
		}
		return 0
	}
	if rec.isRoot {
		return 0
	}
	delete(t.byKey, rec.key)
	if rec.lruElem != nil {
		t.lru.Remove(rec.lruElem)
		rec.lruElem = nil
	}
	if fds := t.takeUnlinkedLocked(rec); len(fds) > 0 {
		return fds[0]
	}
	return 0
}

func (t *handleTable) InvalidatePath(core *layeredfs.Core, viewID ViewID, qpath layeredfs.QualifiedPath) {
	t.mu.Lock()
	pk := makePathKey(viewID, qpath)
	var toClose layeredfs.HandleID
	if rec, ok := t.byPath[pk]; ok {
		toClose = t.dropRecordPathLocked(rec, pk, qpath)
	}
	t.mu.Unlock()
	if core != nil && toClose != 0 {
		_ = core.CloseHandle(toClose)
	}
}
