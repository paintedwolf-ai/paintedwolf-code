package nfsserver

import (
	"container/list"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

const (
	handleMagic   uint32 = 0x4C594653 // "LYFS"
	handleVersion uint8  = 1
	rootSlotProj  uint8  = 1
	rootSlotWork  uint8  = 2
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
	root       RootID
	viewHash   uint64
	nodeID     layeredfs.NodeID
	generation uint64
}

type unlinkedFile struct {
	fd   layeredfs.HandleID
	meta layeredfs.EntryAttr
}

type handleRecord struct {
	wire           [HandleSize]byte
	key            handleKey
	viewID         ViewID
	qpath          layeredfs.QualifiedPath
	generation     uint64
	isRoot         bool
	ownerCreatedRO bool
	createVerf     *[8]byte
	unlinked       *unlinkedFile
	lruElem        *list.Element
}

type handleTable struct {
	mu          sync.RWMutex
	secret      [32]byte
	maxHandles  int
	activeViews map[uint64]ViewID
	byKey       map[handleKey]*handleRecord
	byPath      map[pathKey]*handleRecord
	lru         *list.List
}

func newHandleTable(secret [32]byte, maxHandles int) *handleTable {
	if maxHandles <= 0 {
		maxHandles = DefaultMaxHandles
	}
	return &handleTable{
		secret:      secret,
		maxHandles:  maxHandles,
		activeViews: make(map[uint64]ViewID),
		byKey:       make(map[handleKey]*handleRecord),
		byPath:      make(map[pathKey]*handleRecord),
		lru:         list.New(),
	}
}

func computeViewHash(viewID ViewID) uint64 {
	sum := sha256.Sum256([]byte(viewID))
	v := binary.BigEndian.Uint64(sum[:8])
	if v == 0 {
		return 1
	}
	return v
}

func encodeWireHandle(secret [32]byte, vHash uint64, root RootID, nodeID layeredfs.NodeID, gen uint64) [HandleSize]byte {
	var out [HandleSize]byte
	binary.BigEndian.PutUint32(out[0:4], handleMagic)
	out[4] = handleVersion
	if root == WorktreeRoot {
		out[5] = rootSlotWork
	} else {
		out[5] = rootSlotProj
	}
	binary.BigEndian.PutUint64(out[8:16], vHash)
	binary.BigEndian.PutUint64(out[16:24], uint64(nodeID))
	binary.BigEndian.PutUint64(out[24:32], gen)

	mac := hmac.New(sha256.New, secret[:])
	_, _ = mac.Write(out[0:32])
	digest := mac.Sum(nil)
	copy(out[32:56], digest[:24])
	return out
}

func decodeWireHandle(secret [32]byte, raw []byte) (wireHeader, error) {
	if len(raw) != HandleSize {
		return wireHeader{}, ErrForgedHandle
	}
	if binary.BigEndian.Uint32(raw[0:4]) != handleMagic || raw[4] != handleVersion {
		return wireHeader{}, ErrForgedHandle
	}
	var root RootID
	switch raw[5] {
	case rootSlotProj:
		root = ProjectRoot
	case rootSlotWork:
		root = WorktreeRoot
	default:
		return wireHeader{}, ErrForgedHandle
	}
	mac := hmac.New(sha256.New, secret[:])
	_, _ = mac.Write(raw[0:32])
	if !hmac.Equal(raw[32:56], mac.Sum(nil)[:24]) {
		return wireHeader{}, ErrForgedHandle
	}
	return wireHeader{
		root: root, viewHash: binary.BigEndian.Uint64(raw[8:16]),
		nodeID: layeredfs.NodeID(binary.BigEndian.Uint64(raw[16:24])), generation: binary.BigEndian.Uint64(raw[24:32]),
	}, nil
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
		if rec.unlinked != nil && rec.unlinked.fd != 0 {
			toClose = append(toClose, rec.unlinked.fd)
			rec.unlinked.fd = 0
		}
		if rec.unlinked == nil {
			delete(t.byPath, makePathKey(rec.viewID, rec.qpath))
		}
		if rec.lruElem != nil {
			t.lru.Remove(rec.lruElem)
			rec.lruElem = nil
		}
		delete(t.byKey, k)
	}
	t.mu.Unlock()

	if core != nil {
		for _, fd := range toClose {
			_ = core.CloseHandle(fd)
		}
	}
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

func (t *handleTable) Ensure(
	viewID ViewID, qpath layeredfs.QualifiedPath, meta layeredfs.EntryAttr, isRoot, ownerCreatedRO bool,
) [HandleSize]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	qpath.Rel = cleanRel(qpath.Rel)
	vHash := computeViewHash(viewID)
	t.activeViews[vHash] = viewID
	hk := handleKey{viewHash: vHash, nodeID: meta.NodeID}
	pk := makePathKey(viewID, qpath)
	gen := uint64(meta.Generation)
	if existing, ok := t.byKey[hk]; ok {
		if existing.unlinked == nil && existing.qpath != qpath {
			delete(t.byPath, makePathKey(existing.viewID, existing.qpath))
		}
		existing.qpath, existing.unlinked = qpath, nil
		if existing.generation != gen {
			existing.generation = gen
			existing.wire = encodeWireHandle(t.secret, vHash, qpath.RootID, meta.NodeID, gen)
		}
		existing.ownerCreatedRO = existing.ownerCreatedRO || ownerCreatedRO
		existing.isRoot = existing.isRoot || isRoot
		t.byPath[pk] = existing
		if existing.lruElem != nil {
			t.lru.MoveToFront(existing.lruElem)
		}
		return existing.wire
	}
	if staleAtPath, ok := t.byPath[pk]; ok && staleAtPath.key != hk {
		if staleAtPath.unlinked == nil && staleAtPath.generation == 1 {
			if staleAtPath.lruElem != nil {
				t.lru.MoveToFront(staleAtPath.lruElem)
			}
			return staleAtPath.wire
		}
		delete(t.byPath, pk)
	}
	t.evictIfNeededLocked()
	wire := encodeWireHandle(t.secret, vHash, qpath.RootID, meta.NodeID, gen)
	rec := &handleRecord{
		wire: wire, key: hk, viewID: viewID, qpath: qpath,
		generation: gen, isRoot: isRoot, ownerCreatedRO: ownerCreatedRO,
	}
	rec.lruElem = t.lru.PushFront(rec)
	t.byKey[hk], t.byPath[pk] = rec, rec
	return wire
}

func (t *handleTable) evictIfNeededLocked() {
	for len(t.byKey) >= t.maxHandles && t.lru.Len() > 0 {
		back := t.lru.Back()
		if back == nil {
			break
		}
		rec := back.Value.(*handleRecord)
		if rec.isRoot || (rec.unlinked != nil && rec.unlinked.fd != 0) {
			// Keep root and open-unlinked handles pinned; stop if all remaining entries are pinned.
			if t.lru.Len() <= 4 {
				break
			}
			t.lru.MoveToFront(back)
			continue
		}
		t.lru.Remove(back)
		rec.lruElem = nil
		delete(t.byKey, rec.key)
		if rec.unlinked == nil {
			delete(t.byPath, makePathKey(rec.viewID, rec.qpath))
		}
	}
}

func (t *handleTable) Resolve(raw []byte, expectedViewHash uint64) (*handleRecord, error) {
	hdr, err := decodeWireHandle(t.secret, raw)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if expectedViewHash != 0 && hdr.viewHash != expectedViewHash {
		return nil, ErrCrossViewHandle
	}
	if _, active := t.activeViews[hdr.viewHash]; !active {
		return nil, ErrStaleHandle
	}
	hk := handleKey{viewHash: hdr.viewHash, nodeID: hdr.nodeID}
	rec, ok := t.byKey[hk]
	if !ok || rec.generation != hdr.generation {
		return nil, ErrStaleHandle
	}
	if rec.lruElem != nil {
		t.lru.MoveToFront(rec.lruElem)
	}
	clone := *rec
	return &clone, nil
}

func (t *handleTable) LookupByPath(viewID ViewID, qpath layeredfs.QualifiedPath) (*handleRecord, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	rec, ok := t.byPath[makePathKey(viewID, qpath)]
	if !ok {
		return nil, false
	}
	clone := *rec
	return &clone, true
}

func (t *handleTable) SetOwnerCreatedRO(hk handleKey, ro bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if rec, ok := t.byKey[hk]; ok {
		rec.ownerCreatedRO = ro
	}
}

func (t *handleTable) SetCreateVerifier(hk handleKey, verf [8]byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if rec, ok := t.byKey[hk]; ok {
		v := verf
		rec.createVerf = &v
	}
}

func (t *handleTable) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.byKey)
}
