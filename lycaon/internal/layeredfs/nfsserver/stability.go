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
	seq    uint64
	offset uint64
	data   []byte
}

type unstableFileState struct {
	rec         handleRecord
	chunks      []unstableChunk
	flushingSeq uint64
	maxEnd      uint64
	mtime       time.Time
}

type stabilityLimits struct {
	maxBytesPerView  int
	maxChunksPerView int
	maxTotalBytes    int
}

type stabilityEngine struct {
	mu       sync.Mutex
	verf     [8]byte
	seq      uint64
	chunkSeq uint64
	limits   stabilityLimits
	pending  map[handleKey]*unstableFileState
	flushing map[handleKey]chan struct{}
	sidecars *sidecarTable
}

func newStabilityEngine(cfg Config) *stabilityEngine {
	e := &stabilityEngine{
		limits: stabilityLimits{
			maxBytesPerView:  cfg.MaxUnstableBytesPerView,
			maxChunksPerView: cfg.MaxUnstableChunksPerView,
			maxTotalBytes:    cfg.MaxTotalUnstableBytes,
		},
		pending:  make(map[handleKey]*unstableFileState),
		flushing: make(map[handleKey]chan struct{}),
		sidecars: newSidecarTable(),
	}
	rotateVerifierLocked(e)
	return e
}

func rotateVerifierLocked(e *stabilityEngine) [8]byte {
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

func (e *stabilityEngine) ResetUnstableState() [8]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending = make(map[handleKey]*unstableFileState)
	return rotateVerifierLocked(e)
}

func (e *stabilityEngine) ReleaseView(vHash uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
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

func effectiveMetaLocked(e *stabilityEngine, hk handleKey, base layeredfs.EntryAttr) layeredfs.EntryAttr {
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

func (e *stabilityEngine) EffectiveMeta(hk handleKey, base layeredfs.EntryAttr) layeredfs.EntryAttr {
	e.mu.Lock()
	defer e.mu.Unlock()
	return effectiveMetaLocked(e, hk, base)
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

func tryStageLocked(
	e *stabilityEngine, rec *handleRecord, baseMeta layeredfs.EntryAttr, offset uint64, data []byte,
) (layeredfs.EntryAttr, [8]byte, bool) {
	now := time.Now()
	if len(data) == 0 {
		baseMeta.ModTime, baseMeta.CTime = now, now
		return baseMeta, e.verf, true
	}
	st := e.pending[rec.key]
	var curChunks []unstableChunk
	var flushingSeq uint64
	if st != nil {
		curChunks, flushingSeq = st.chunks, st.flushingSeq
	}
	newSeq := e.chunkSeq + 1
	added := unstableChunk{seq: newSeq, offset: offset, data: append([]byte(nil), data...)}
	nextChunks := compactAndInsertChunk(curChunks, added, flushingSeq)
	oldBytes, oldCount := chunksByteCount(curChunks), len(curChunks)
	newBytes, newCount := chunksByteCount(nextChunks), len(nextChunks)
	viewBytes, viewChunks, totalBytes := pendingStatsLocked(e, rec.key.viewHash)
	if viewBytes-oldBytes+newBytes > e.limits.maxBytesPerView ||
		viewChunks-oldCount+newCount > e.limits.maxChunksPerView ||
		totalBytes-oldBytes+newBytes > e.limits.maxTotalBytes {
		return layeredfs.EntryAttr{}, e.verf, false
	}
	e.chunkSeq = newSeq
	if st == nil {
		st = &unstableFileState{rec: *rec, maxEnd: uint64(max(baseMeta.Size, 0))}
		e.pending[rec.key] = st
	} else {
		st.rec = *rec
	}
	st.chunks = nextChunks
	if end := offset + uint64(len(data)); end > st.maxEnd {
		st.maxEnd = end
	}
	st.mtime = now
	if int64(st.maxEnd) > baseMeta.Size {
		baseMeta.Size = int64(st.maxEnd)
	}
	baseMeta.ModTime, baseMeta.CTime = now, now
	return baseMeta, e.verf, true
}

func (e *stabilityEngine) StageUnstable(
	core *layeredfs.Core, handles *handleTable, fault *FaultInjector,
	rec *handleRecord, baseMeta layeredfs.EntryAttr, offset uint64, data []byte,
) (layeredfs.EntryAttr, WriteStability, [8]byte, error) {
	e.mu.Lock()
	if meta, verf, ok := tryStageLocked(e, rec, baseMeta, offset, data); ok {
		e.mu.Unlock()
		return meta, Unstable, verf, nil
	}
	e.mu.Unlock()
	if err := e.FlushView(context.Background(), core, handles, fault, rec.viewID); err != nil {
		return layeredfs.EntryAttr{}, Unstable, e.WriteVerifier(), err
	}
	e.mu.Lock()
	if meta, verf, ok := tryStageLocked(e, rec, baseMeta, offset, data); ok {
		e.mu.Unlock()
		return meta, Unstable, verf, nil
	}
	e.mu.Unlock()
	meta, verf, err := e.WriteStable(core, handles, fault, rec, offset, data, FileSync)
	return meta, FileSync, verf, err
}

func (e *stabilityEngine) WriteStable(
	core *layeredfs.Core, handles *handleTable, fault *FaultInjector,
	rec *handleRecord, offset uint64, data []byte, stability WriteStability,
) (layeredfs.EntryAttr, [8]byte, error) {
	acquireNodeFlush(e, rec.key)
	defer releaseNodeFlush(e, rec.key)
	e.mu.Lock()
	chunks, flushedSeq, _ := snapshotFlushLocked(e, rec.key)
	e.mu.Unlock()
	chunks = append(chunks, unstableChunk{offset: offset, data: data})
	meta, err := flushChunksToCore(core, handles, e.sidecars, fault, rec, chunks)
	e.mu.Lock()
	verf := finishFlushLocked(e, rec.key, flushedSeq, err)
	if err == nil {
		meta = effectiveMetaLocked(e, rec.key, meta)
	}
	e.mu.Unlock()
	if err == nil {
		e.absorbFlushed(core, rec)
	}
	return meta, verf, err
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
	acquireNodeFlush(e, rec.key)
	defer releaseNodeFlush(e, rec.key)
	e.mu.Lock()
	chunks, flushedSeq, verf := snapshotFlushLocked(e, rec.key)
	e.mu.Unlock()
	if len(chunks) == 0 {
		if fault != nil && fault.OnSync != nil {
			if err := fault.OnSync(rec.viewID, rec.qpath); err != nil {
				return baseMeta, verf, err
			}
		}
		return baseMeta, verf, nil
	}
	meta, err := flushChunksToCore(core, handles, e.sidecars, fault, rec, chunks)
	e.mu.Lock()
	verf = finishFlushLocked(e, rec.key, flushedSeq, err)
	if err == nil {
		meta = effectiveMetaLocked(e, rec.key, meta)
	} else {
		meta = baseMeta
	}
	e.mu.Unlock()
	if err == nil {
		e.absorbFlushed(core, rec)
	}
	return meta, verf, err
}

// FlushKey settles any acknowledged UNSTABLE bytes for one object.
func (e *stabilityEngine) FlushKey(core *layeredfs.Core, handles *handleTable, fault *FaultInjector, hk handleKey) error {
	return flushPendingKey(e, core, handles, fault, hk)
}

func (e *stabilityEngine) FlushView(
	ctx context.Context, core *layeredfs.Core, handles *handleTable, fault *FaultInjector, viewID ViewID,
) error {
	vHash := handles.ViewHash(viewID)
	e.mu.Lock()
	var keys []handleKey
	for hk, st := range e.pending {
		if hk.viewHash == vHash && len(st.chunks) > 0 {
			keys = append(keys, hk)
		}
	}
	e.mu.Unlock()
	for _, hk := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := flushPendingKey(e, core, handles, fault, hk); err != nil {
			return err
		}
	}
	return nil
}
