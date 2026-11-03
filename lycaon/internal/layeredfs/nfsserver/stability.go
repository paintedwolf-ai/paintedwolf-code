package nfsserver

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

type unstableChunk struct {
	offset uint64
	data   []byte
}

type unstableFileState struct {
	rec    handleRecord
	chunks []unstableChunk
	maxEnd uint64
	mtime  time.Time
}

type stabilityEngine struct {
	mu      sync.Mutex
	verf    [8]byte
	seq     uint64
	pending map[handleKey]*unstableFileState
}

func newStabilityEngine() *stabilityEngine {
	e := &stabilityEngine{pending: make(map[handleKey]*unstableFileState)}
	e.rotateVerifierLocked()
	return e
}

func (e *stabilityEngine) rotateVerifierLocked() [8]byte {
	e.seq++
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		binary.BigEndian.PutUint64(b[:], uint64(time.Now().UnixNano())^e.seq)
	}
	v := binary.BigEndian.Uint64(b[:]) ^ (e.seq * 0x9e3779b97f4a7c15)
	if v == 0 {
		v = e.seq
	}
	binary.BigEndian.PutUint64(e.verf[:], v)
	return e.verf
}

func (e *stabilityEngine) WriteVerifier() [8]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.verf
}

// ResetUnstableState discards uncommitted UNSTABLE buffers and rotates the write
// verifier to simulate a server restart between UNSTABLE writes and COMMIT.
func (e *stabilityEngine) ResetUnstableState() [8]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending = make(map[handleKey]*unstableFileState)
	return e.rotateVerifierLocked()
}

func (e *stabilityEngine) ReleaseView(viewID ViewID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	vHash := computeViewHash(viewID)
	for hk := range e.pending {
		if hk.viewHash == vHash {
			delete(e.pending, hk)
		}
	}
}

func (e *stabilityEngine) DiscardKey(hk handleKey) {
	e.mu.Lock()
	delete(e.pending, hk)
	e.mu.Unlock()
}

func (e *stabilityEngine) UpdateRecPath(hk handleKey, qpath layeredfs.QualifiedPath) {
	e.mu.Lock()
	if st, ok := e.pending[hk]; ok {
		st.rec.qpath = qpath
	}
	e.mu.Unlock()
}

func (e *stabilityEngine) EffectiveMeta(hk handleKey, base layeredfs.EntryAttr) layeredfs.EntryAttr {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.pending[hk]
	if !ok || len(st.chunks) == 0 {
		return base
	}
	if int64(st.maxEnd) > base.Size {
		base.Size = int64(st.maxEnd)
	}
	if st.mtime.After(base.ModTime) {
		base.ModTime, base.CTime = st.mtime, st.mtime
	}
	return base
}

func (e *stabilityEngine) OverlayRead(hk handleKey, offset uint64, count uint32, baseData []byte, baseSize int64) ([]byte, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.pending[hk]
	if !ok || len(st.chunks) == 0 {
		return baseData, offset+uint64(len(baseData)) >= uint64(max(baseSize, 0))
	}
	effectiveSize := max(uint64(max(baseSize, 0)), st.maxEnd)
	if offset >= effectiveSize {
		return nil, true
	}
	avail := min(effectiveSize-offset, uint64(count))
	out := make([]byte, avail)
	copy(out, baseData)
	reqEnd := offset + avail
	for _, ch := range st.chunks {
		chEnd := ch.offset + uint64(len(ch.data))
		if chEnd <= offset || ch.offset >= reqEnd {
			continue
		}
		start, end := max(ch.offset, offset), min(chEnd, reqEnd)
		copy(out[start-offset:end-offset], ch.data[start-ch.offset:end-ch.offset])
	}
	return out, offset+avail >= effectiveSize
}

func (e *stabilityEngine) StageUnstable(
	rec *handleRecord, baseMeta layeredfs.EntryAttr, offset uint64, data []byte,
) (layeredfs.EntryAttr, [8]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.pending[rec.key]
	now := time.Now()
	if !ok {
		st = &unstableFileState{rec: *rec, maxEnd: uint64(max(baseMeta.Size, 0))}
		e.pending[rec.key] = st
	} else {
		st.rec = *rec
	}
	buf := append([]byte(nil), data...)
	st.chunks = append(st.chunks, unstableChunk{offset: offset, data: buf})
	if end := offset + uint64(len(buf)); end > st.maxEnd {
		st.maxEnd = end
	}
	st.mtime = now
	if int64(st.maxEnd) > baseMeta.Size {
		baseMeta.Size = int64(st.maxEnd)
	}
	baseMeta.ModTime, baseMeta.CTime = now, now
	return baseMeta, e.verf, nil
}

func (e *stabilityEngine) WriteStable(
	core *layeredfs.Core, handles *handleTable, fault *FaultInjector,
	rec *handleRecord, offset uint64, data []byte, stability WriteStability,
) (layeredfs.EntryAttr, [8]byte, error) {
	e.mu.Lock()
	var chunks []unstableChunk
	if st, ok := e.pending[rec.key]; ok {
		chunks = append(chunks, st.chunks...)
	}
	verf := e.verf
	e.mu.Unlock()
	chunks = append(chunks, unstableChunk{offset: offset, data: data})
	meta, err := flushChunksToCore(core, handles, fault, rec, chunks, stability == FileSync)
	if err != nil {
		return layeredfs.EntryAttr{}, verf, err
	}
	e.mu.Lock()
	delete(e.pending, rec.key)
	verf = e.verf
	e.mu.Unlock()
	return meta, verf, nil
}

func (e *stabilityEngine) Commit(
	core *layeredfs.Core, handles *handleTable, fault *FaultInjector,
	rec *handleRecord, baseMeta layeredfs.EntryAttr, offset uint64, count uint32,
) (layeredfs.EntryAttr, [8]byte, error) {
	if fault != nil && fault.OnCommit != nil {
		if err := fault.OnCommit(rec.viewID, rec.qpath, offset, count); err != nil {
			return baseMeta, e.WriteVerifier(), err
		}
	}
	e.mu.Lock()
	var chunks []unstableChunk
	if st, ok := e.pending[rec.key]; ok && len(st.chunks) > 0 {
		chunks = append([]unstableChunk(nil), st.chunks...)
	}
	verf := e.verf
	e.mu.Unlock()
	if len(chunks) == 0 {
		if fault != nil && fault.OnSync != nil {
			if err := fault.OnSync(rec.viewID, rec.qpath); err != nil {
				return baseMeta, verf, err
			}
		}
		return baseMeta, verf, nil
	}
	meta, err := flushChunksToCore(core, handles, fault, rec, chunks, true)
	if err != nil {
		return baseMeta, verf, err
	}
	e.mu.Lock()
	delete(e.pending, rec.key)
	verf = e.verf
	e.mu.Unlock()
	return meta, verf, nil
}

func (e *stabilityEngine) FlushView(
	ctx context.Context, core *layeredfs.Core, handles *handleTable, fault *FaultInjector, viewID ViewID,
) error {
	vHash := computeViewHash(viewID)
	e.mu.Lock()
	var states []*unstableFileState
	for hk, st := range e.pending {
		if hk.viewHash == vHash && len(st.chunks) > 0 {
			states = append(states, &unstableFileState{rec: st.rec, chunks: append([]unstableChunk(nil), st.chunks...)})
		}
	}
	e.mu.Unlock()
	for _, st := range states {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := flushChunksToCore(core, handles, fault, &st.rec, st.chunks, true); err != nil {
			return err
		}
		e.mu.Lock()
		delete(e.pending, st.rec.key)
		e.mu.Unlock()
	}
	return nil
}
