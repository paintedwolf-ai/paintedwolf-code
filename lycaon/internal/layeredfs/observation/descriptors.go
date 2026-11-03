package observation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// OpenHeldDescriptor opens a read-only handle via Core and binds a HeldDescriptorObservation
// to the exact object identity and content digest opened at this instant.
func (d *DescriptorTracker) OpenHeldDescriptor(qpath layeredfs.QualifiedPath) (layeredfs.HandleID, HeldDescriptorObservation, error) {
	t := d.tr
	qpath = normalizeQPath(qpath)
	t.mu.RLock()
	core := t.core
	t.mu.RUnlock()
	if core == nil {
		return 0, HeldDescriptorObservation{}, ErrNilCore
	}

	hid, attr, err := core.Open(qpath, layeredfs.OpenReadOnly, 0)
	if err != nil {
		t.ObserveLookupResult(qpath, layeredfs.EntryAttr{}, err)
		return 0, HeldDescriptorObservation{}, err
	}

	var buf []byte
	if attr.Size > 0 {
		buf = make([]byte, attr.Size)
		n, _ := core.ReadHandle(hid, buf, 0)
		buf = buf[:n]
	}
	sum := sha256.Sum256(buf)
	digest := hex.EncodeToString(sum[:])

	var ino, dev uint64
	t.mu.RLock()
	if rootSpec, ok := t.rootsByID[qpath.RootID]; ok && rootSpec.TrunkPath != "" && attr.Layer == layeredfs.LayerTrunk {
		if info, sErr := os.Lstat(filepath.Join(rootSpec.TrunkPath, filepath.FromSlash(qpath.Rel))); sErr == nil {
			if sys, ok := info.Sys().(*syscall.Stat_t); ok && sys != nil {
				ino = uint64(sys.Ino)
				dev = uint64(sys.Dev)
			}
		}
	}
	t.mu.RUnlock()

	held := d.RecordHeldDescriptorOpen(hid, qpath, attr, digest, ino, dev)
	return hid, held, nil
}

// RecordHeldDescriptorOpen records an open descriptor's bound identity and content digest.
func (d *DescriptorTracker) RecordHeldDescriptorOpen(
	hid layeredfs.HandleID,
	qpath layeredfs.QualifiedPath,
	attr layeredfs.EntryAttr,
	openedDigest string,
	openedIno uint64,
	openedDev uint64,
) HeldDescriptorObservation {
	t := d.tr
	qpath = normalizeQPath(qpath)
	t.mu.Lock()
	defer t.mu.Unlock()

	gen := rootGenLocked(t, qpath.RootID)
	ev := layeredfs.ObservationEvent{
		ViewID:     t.viewID,
		Path:       qpath,
		Layer:      attr.Layer,
		NodeID:     attr.NodeID,
		Generation: attr.Generation,
		Kind:       layeredfs.ObserveRead,
		Size:       attr.Size,
		ModTimeNS:  attr.ModTime.UnixNano(),
		SHA256:     openedDigest,
		Ino:        openedIno,
		Dev:        openedDev,
	}
	obs := buildBaseObservationLocked(t, qpath, ev, ProvenanceServerFSRequest, t.facts.AttributionCompleteness)
	obs.SHA256 = openedDigest
	obs.Ino = openedIno
	obs = appendObservationLocked(t, obs)

	held := HeldDescriptorObservation{
		HandleID:         hid,
		ObservationID:    obs.ObservationID,
		Path:             qpath,
		Layer:            attr.Layer,
		NodeID:           attr.NodeID,
		OpenedDigest:     openedDigest,
		OpenedSize:       attr.Size,
		OpenedIno:        openedIno,
		OpenedModTimeNS:  attr.ModTime.UnixNano(),
		SourceGeneration: gen,
	}
	t.store.heldByHandle[hid] = held
	if attr.NodeID != 0 {
		t.store.heldByNode[nodeIdentityKey{nodeID: attr.NodeID, gen: attr.Generation}] = held
	}
	return held
}

// HeldDescriptor returns the recorded HeldDescriptorObservation for hid.
func (d *DescriptorTracker) HeldDescriptor(hid layeredfs.HandleID) (HeldDescriptorObservation, bool) {
	t := d.tr
	t.mu.RLock()
	defer t.mu.RUnlock()
	h, ok := t.store.heldByHandle[hid]
	return h, ok
}

// ReadHeldDescriptor reads from an open descriptor via Core while preserving the descriptor's
// distinct observed version even after pathname replacement on trunk.
func (d *DescriptorTracker) ReadHeldDescriptor(hid layeredfs.HandleID, maxBytes int64) ([]byte, HeldDescriptorObservation, error) {
	t := d.tr
	t.mu.RLock()
	core := t.core
	held, hasHeld := t.store.heldByHandle[hid]
	t.mu.RUnlock()
	if core == nil {
		return nil, HeldDescriptorObservation{}, ErrNilCore
	}
	if maxBytes <= 0 {
		if hasHeld && held.OpenedSize > 0 {
			maxBytes = held.OpenedSize
		} else {
			maxBytes = 64 * 1024
		}
	}
	buf := make([]byte, maxBytes)
	n, err := core.ReadHandle(hid, buf, 0)
	if err != nil && n == 0 {
		return nil, held, err
	}
	data := buf[:n]
	sum := sha256.Sum256(data)
	held.OpenedDigest = hex.EncodeToString(sum[:])
	held.OpenedSize = int64(n)
	return data, held, nil
}

func enrichTrunkObservationLocked(t *Tracker, obs *SourceObservation, ev layeredfs.ObservationEvent) {
	if held, ok := t.store.heldByNode[nodeIdentityKey{nodeID: ev.NodeID, gen: ev.Generation}]; ok && ev.NodeID != 0 {
		obs.SHA256 = held.OpenedDigest
		obs.Size = held.OpenedSize
		obs.Ino = held.OpenedIno
		obs.ModTimeNS = held.OpenedModTimeNS
		obs.SourceGeneration = held.SourceGeneration
		return
	}
	if st, ok := t.coherence.trunkState[obs.Path]; ok && !st.deleted {
		if obs.Ino == 0 {
			obs.Ino = st.ino
		}
		if obs.SHA256 == "" && obs.Kind == layeredfs.ObserveRead {
			obs.SHA256 = st.sha256
		}
		if obs.ModTimeNS == 0 {
			obs.ModTimeNS = st.modTimeNS
		}
	}
	rootSpec, ok := t.rootsByID[obs.Path.RootID]
	if !ok || rootSpec.TrunkPath == "" {
		return
	}
	absPath := filepath.Join(rootSpec.TrunkPath, filepath.FromSlash(obs.Path.Rel))
	info, err := os.Lstat(absPath)
	if err != nil {
		return
	}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok && sys != nil && obs.Ino == 0 {
		obs.Ino = uint64(sys.Ino)
	}
	if obs.ModTimeNS == 0 {
		obs.ModTimeNS = info.ModTime().UnixNano()
	}
	if obs.Kind == layeredfs.ObserveRead && obs.SHA256 == "" && info.Mode().IsRegular() {
		if data, rErr := os.ReadFile(absPath); rErr == nil {
			sum := sha256.Sum256(data)
			obs.SHA256 = hex.EncodeToString(sum[:])
			obs.Size = int64(len(data))
		}
	}
}

func buildBaseObservationLocked(
	t *Tracker,
	qpath layeredfs.QualifiedPath,
	ev layeredfs.ObservationEvent,
	prov ObservationProvenance,
	completeness AttributionCompleteness,
) SourceObservation {
	t.store.nextObsSeq++
	obsID := fmt.Sprintf("obs-%s-%06d", t.viewID, t.store.nextObsSeq)
	layer := ev.Layer
	if layer == "" {
		layer = layeredfs.LayerTrunk
	}
	obs := SourceObservation{
		ObservationID:    obsID,
		Path:             qpath,
		Layer:            layer,
		Kind:             ev.Kind,
		Provenance:       prov,
		Completeness:     completeness,
		SHA256:           ev.SHA256,
		Size:             ev.Size,
		ModTimeNS:        ev.ModTimeNS,
		Ino:              ev.Ino,
		SourceGeneration: rootGenLocked(t, qpath.RootID),
	}
	if layer == layeredfs.LayerTrunk {
		enrichTrunkObservationLocked(t, &obs, ev)
	}
	if reasons := t.coherence.overflowByRoot[qpath.RootID]; len(reasons) > 0 && layer == layeredfs.LayerTrunk && prov != ProvenanceExactNativeRead {
		obs.Uncertain = true
		obs.Provenance = ProvenanceUncertainOverflow
	}
	return obs
}

func appendObservationLocked(t *Tracker, obs SourceObservation) SourceObservation {
	idx := len(t.store.observations)
	t.store.observations = append(t.store.observations, obs)
	t.store.obsByPath[obs.Path] = append(t.store.obsByPath[obs.Path], idx)
	t.store.obsByID[obs.ObservationID] = idx
	return obs
}
