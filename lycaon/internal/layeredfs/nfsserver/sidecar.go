package nfsserver

import (
	"bytes"
	"container/list"
	"os"
	"path"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

const (
	// maxAbsorbedSidecarBytes bounds one ._ file taken into its target's attributes; a larger one
	// stays an ordinary private file.
	maxAbsorbedSidecarBytes = 8 << 20
	// The table's bounds. An entry that falls off costs only the exact bytes and handle of a sidecar
	// the client is no longer using: attributes stay on the target and synthesize a canonical file.
	maxHeldSidecars     = 4096
	maxHeldSidecarBytes = 64 << 20

	synthSidecarIDMask layeredfs.NodeID = 0xAD00000000000001
	synthSidecarMode   os.FileMode      = 0o644
)

type heldSidecarKey struct {
	core   *layeredfs.Core
	target layeredfs.NodeID
}

// heldSidecar is a ._ file now stored as its target's attributes. The client may hold a handle to it
// and offsets parsed from its layout, so it keeps reading the same identity and the same bytes for as
// long as the attributes are the ones those bytes decode to.
type heldSidecar struct {
	key    heldSidecarKey
	id     layeredfs.NodeID
	mode   os.FileMode
	raw    []byte
	xattrs map[string][]byte
}

type placeholderKey struct {
	core    *layeredfs.Core
	sidecar layeredfs.QualifiedPath
}

type sidecarTable struct {
	mu    sync.Mutex
	byKey map[heldSidecarKey]*list.Element
	lru   *list.List
	bytes int
	// moved names where the client put an open file it was asked to delete, by that file's ._ name.
	// The file still exists under the placeholder, and the kernel's write of its ._ file may land later.
	moved map[placeholderKey]layeredfs.QualifiedPath
	// materializing covers a ._ file between its creation and taking its identity. A reader in that
	// window would see a new object at the name and report the client's handle stale.
	materializing sync.RWMutex
}

func newSidecarTable() *sidecarTable {
	return &sidecarTable{
		byKey: make(map[heldSidecarKey]*list.Element), lru: list.New(),
		moved: make(map[placeholderKey]layeredfs.QualifiedPath),
	}
}

func (t *sidecarTable) hold(entry *heldSidecar) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dropLocked(entry.key)
	t.byKey[entry.key] = t.lru.PushFront(entry)
	t.bytes += len(entry.raw)
	for t.lru.Len() > maxHeldSidecars || t.bytes > maxHeldSidecarBytes {
		t.dropLocked(t.lru.Back().Value.(*heldSidecar).key)
	}
}

func (t *sidecarTable) dropLocked(key heldSidecarKey) {
	if elem, ok := t.byKey[key]; ok {
		t.bytes -= len(elem.Value.(*heldSidecar).raw)
		t.lru.Remove(elem)
		delete(t.byKey, key)
	}
}

func (t *sidecarTable) drop(key heldSidecarKey) {
	t.mu.Lock()
	t.dropLocked(key)
	t.mu.Unlock()
}

// notePlaceholder records that the file sidecar belongs to now lives at placeholder.
func (t *sidecarTable) notePlaceholder(core *layeredfs.Core, sidecar, placeholder layeredfs.QualifiedPath) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.moved) >= maxHeldSidecars {
		clear(t.moved)
	}
	t.moved[placeholderKey{core, sidecar}] = placeholder
}

// absorbTarget finds the object a ._ file describes: the file at its name, or the placeholder that
// file went to. A new file at the name supersedes the placeholder.
func (t *sidecarTable) absorbTarget(core *layeredfs.Core, sidecar, named layeredfs.QualifiedPath) (layeredfs.QualifiedPath, layeredfs.EntryAttr, bool) {
	key := placeholderKey{core, sidecar}
	if target, err := core.Lstat(named); err == nil {
		t.mu.Lock()
		delete(t.moved, key)
		t.mu.Unlock()
		return named, target, true
	}
	t.mu.Lock()
	placeholder, moved := t.moved[key]
	t.mu.Unlock()
	if !moved {
		return layeredfs.QualifiedPath{}, layeredfs.EntryAttr{}, false
	}
	target, err := core.Lstat(placeholder)
	return placeholder, target, err == nil
}

func (t *sidecarTable) releaseCore(core *layeredfs.Core) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key := range t.moved {
		if key.core == core {
			delete(t.moved, key)
		}
	}
	for key := range t.byKey {
		if key.core == core {
			t.dropLocked(key)
		}
	}
}

func (t *sidecarTable) heldFor(core *layeredfs.Core, target layeredfs.EntryAttr) *heldSidecar {
	t.mu.Lock()
	defer t.mu.Unlock()
	elem, ok := t.byKey[heldSidecarKey{core, target.StableID()}]
	if !ok {
		return nil
	}
	entry := elem.Value.(*heldSidecar)
	if !sameXattrs(entry.xattrs, target.Xattrs) {
		return nil
	}
	t.lru.MoveToFront(elem)
	return entry
}

// targetIdentity names the object whose presented ._ file carries identity id.
func (t *sidecarTable) targetIdentity(core *layeredfs.Core, id layeredfs.NodeID) layeredfs.NodeID {
	t.mu.Lock()
	defer t.mu.Unlock()
	for elem := t.lru.Front(); elem != nil; elem = elem.Next() {
		if entry := elem.Value.(*heldSidecar); entry.key.core == core && entry.id == id {
			return entry.key.target
		}
	}
	return id ^ synthSidecarIDMask
}

// locate rebuilds a presented ._ file's place from its identity, for a handle that left memory. The
// file has no identity of its own in the view, so the search is for its target.
func (t *sidecarTable) locate(core *layeredfs.Core, id layeredfs.NodeID, parent layeredfs.QualifiedPath, hasParent bool) (layeredfs.QualifiedPath, layeredfs.EntryAttr, bool) {
	target := t.targetIdentity(core, id)
	targetPath, _, found := core.ResolveIdentity(target)
	if !found && hasParent {
		name, ok, err := core.FindChildByIdentity(parent, target)
		if err != nil || !ok {
			return layeredfs.QualifiedPath{}, layeredfs.EntryAttr{}, false
		}
		if targetPath, err = joinChildPath(parent, name); err != nil {
			return layeredfs.QualifiedPath{}, layeredfs.EntryAttr{}, false
		}
		found = true
	}
	if !found {
		return layeredfs.QualifiedPath{}, layeredfs.EntryAttr{}, false
	}
	sidecar := sidecarPathOf(targetPath)
	meta, _, ok := t.synth(core, sidecar)
	if !ok || meta.StableID() != id {
		return layeredfs.QualifiedPath{}, layeredfs.EntryAttr{}, false
	}
	return sidecar, meta, true
}

// synth presents the ._ file for a target whose attributes are held in the view.
func (t *sidecarTable) synth(core *layeredfs.Core, qpath layeredfs.QualifiedPath) (layeredfs.EntryAttr, []byte, bool) {
	targetRel, ok := isAppleDoublePath(qpath.Rel)
	if !ok {
		return layeredfs.EntryAttr{}, nil, false
	}
	target, err := core.Lstat(layeredfs.QualifiedPath{RootID: qpath.RootID, Rel: targetRel})
	if err != nil {
		return layeredfs.EntryAttr{}, nil, false
	}
	id, mode := target.StableID()^synthSidecarIDMask, synthSidecarMode
	var payload []byte
	if held := t.heldFor(core, target); held != nil {
		id, mode, payload = held.id, held.mode, held.raw
	} else if len(target.Xattrs) > 0 {
		payload = EncodeAppleDoubleXattrs([32]byte{}, target.Xattrs)
	} else {
		return layeredfs.EntryAttr{}, nil, false
	}
	return layeredfs.EntryAttr{
		NodeID: id, Generation: 1, Layer: target.Layer,
		Kind: layeredfs.EntryRegular, Mode: mode, Size: int64(len(payload)),
		ModTime: target.ModTime, ATime: target.ATime, CTime: target.CTime, Nlink: 1,
	}, payload, true
}

// settled waits out a materialization of the ._ file at qpath, if that is what it names, until the
// returned function runs.
func (t *sidecarTable) settled(qpath layeredfs.QualifiedPath) func() {
	if _, ok := isAppleDoublePath(qpath.Rel); !ok {
		return func() {}
	}
	t.materializing.RLock()
	return t.materializing.RUnlock
}

// materialize turns a presented ._ file into a real private file, keeping its identity and bytes,
// because the kernel is about to change it through a handle it holds.
func (t *sidecarTable) materialize(core *layeredfs.Core, qpath layeredfs.QualifiedPath) error {
	targetRel, ok := isAppleDoublePath(qpath.Rel)
	if !ok {
		return nil
	}
	t.materializing.Lock()
	defer t.materializing.Unlock()
	if _, err := core.Lstat(qpath); err == nil {
		return nil
	}
	meta, payload, ok := t.synth(core, qpath)
	if !ok {
		return nil
	}
	if _, err := core.WriteFile(qpath, payload, meta.Mode); err != nil {
		return err
	}
	core.AdoptIdentity(qpath, meta.NodeID)
	if target, err := core.Lstat(layeredfs.QualifiedPath{RootID: qpath.RootID, Rel: targetRel}); err == nil {
		t.drop(heldSidecarKey{core, target.StableID()})
	}
	return nil
}

// absorb stores a ._ file as its target's attributes and takes it out of the view. It acts only on
// a file in the kernel's complete layout whose target exists and whose attributes land and read back
// exactly; everything else stays an ordinary private file with its bytes untouched.
func (t *sidecarTable) absorb(core *layeredfs.Core, qpath layeredfs.QualifiedPath) {
	targetRel, ok := isAppleDoublePath(qpath.Rel)
	if !ok {
		return
	}
	if _, nested := isAppleDoublePath(targetRel); nested {
		return
	}
	side, err := core.Lstat(qpath)
	if err != nil || side.Kind != layeredfs.EntryRegular || side.Layer != layeredfs.LayerPrivate ||
		side.Nlink != 1 || side.Size > maxAbsorbedSidecarBytes || !core.AddedInView(qpath) {
		return
	}
	targetPath, target, ok := t.absorbTarget(core, qpath, layeredfs.QualifiedPath{RootID: qpath.RootID, Rel: targetRel})
	if !ok {
		return
	}
	raw, err := core.ReadFile(qpath)
	if err != nil {
		return
	}
	_, xattrs, ok := DecodeAppleDoubleXattrs(raw)
	if !ok || !landXattrs(core, targetPath, target.Xattrs, xattrs) {
		return
	}
	key := heldSidecarKey{core, target.StableID()}
	t.hold(&heldSidecar{key: key, id: side.StableID(), mode: side.Mode, raw: raw, xattrs: xattrs})
	if err := core.Unlink(qpath, false); err != nil {
		t.drop(key)
	}
}

// landXattrs makes want the target's attributes and proves it by reading them back. On failure it
// puts back what it found, since the ._ file stays and keeps meaning what it meant before.
func landXattrs(core *layeredfs.Core, target layeredfs.QualifiedPath, have, want map[string][]byte) bool {
	applyXattrs(core, target, have, want)
	if after, err := core.Lstat(target); err == nil && sameXattrs(after.Xattrs, want) {
		return true
	}
	if after, err := core.Lstat(target); err == nil {
		applyXattrs(core, target, after.Xattrs, have)
	}
	return false
}

func applyXattrs(core *layeredfs.Core, target layeredfs.QualifiedPath, have, want map[string][]byte) {
	for name, value := range want {
		if current, set := have[name]; !set || !bytes.Equal(current, value) {
			_ = core.SetXattr(target, name, value, true)
		}
	}
	for name := range have {
		if _, kept := want[name]; !kept {
			_ = core.RemoveXattr(target, name, true)
		}
	}
}

func sameXattrs(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for name, value := range a {
		other, ok := b[name]
		if !ok || !bytes.Equal(value, other) {
			return false
		}
	}
	return true
}

// clearPresented removes the attributes behind a presented ._ file the client removed, so it does not
// come back: the kernel does this when the last attribute goes.
func (t *sidecarTable) clearPresented(core *layeredfs.Core, qpath layeredfs.QualifiedPath) {
	targetRel, ok := isAppleDoublePath(qpath.Rel)
	if !ok {
		return
	}
	targetPath := layeredfs.QualifiedPath{RootID: qpath.RootID, Rel: targetRel}
	target, err := core.Lstat(targetPath)
	if err != nil {
		return
	}
	t.drop(heldSidecarKey{core, target.StableID()})
	for name := range target.Xattrs {
		_ = core.RemoveXattr(targetPath, name, true)
	}
}

// sidecarPathOf names the ._ file a target's attributes would live in.
func sidecarPathOf(qpath layeredfs.QualifiedPath) layeredfs.QualifiedPath {
	dir, base := path.Dir(qpath.Rel), path.Base(qpath.Rel)
	if dir == "." || dir == "" {
		return layeredfs.QualifiedPath{RootID: qpath.RootID, Rel: "._" + base}
	}
	return layeredfs.QualifiedPath{RootID: qpath.RootID, Rel: dir + "/._" + base}
}

// holdSidecarFlush keeps other changes to a ._ file out while the caller changes it: absorption reads
// the file and then removes it, and anything landing between the two would go with it.
func (e *stabilityEngine) holdSidecarFlush(rec *handleRecord) func() {
	if _, ok := isAppleDoublePath(rec.qpath.Rel); !ok {
		return func() {}
	}
	acquireNodeFlush(e, rec.key)
	return func() { releaseNodeFlush(e, rec.key) }
}

// absorbFlushed takes a ._ file out of the view once every acknowledged byte of it has landed. The
// caller holds the object's flush.
func (e *stabilityEngine) absorbFlushed(core *layeredfs.Core, rec *handleRecord) {
	if rec.unlinked != nil {
		return
	}
	if _, ok := isAppleDoublePath(rec.qpath.Rel); !ok {
		return
	}
	e.mu.Lock()
	st := e.pending[rec.key]
	busy := st != nil && len(st.chunks) > 0
	e.mu.Unlock()
	if !busy {
		e.sidecars.absorb(core, rec.qpath)
	}
}

// absorbAtName reconsiders the ._ file at or for qpath after a name appeared: a sidecar with no target
// waits as a file until the target arrives, and a file renamed onto a sidecar name may now qualify.
func (e *stabilityEngine) absorbAtName(core *layeredfs.Core, handles *handleTable, viewID ViewID, qpath layeredfs.QualifiedPath) {
	sidecar := qpath
	if _, ok := isAppleDoublePath(qpath.Rel); !ok {
		sidecar = sidecarPathOf(qpath)
	}
	rec, known := handles.LookupByPath(viewID, sidecar)
	if !known {
		e.sidecars.absorb(core, sidecar)
		return
	}
	acquireNodeFlush(e, rec.key)
	defer releaseNodeFlush(e, rec.key)
	e.absorbFlushed(core, rec)
}
