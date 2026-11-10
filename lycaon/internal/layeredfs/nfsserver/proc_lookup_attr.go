package nfsserver

import (
	"bytes"
	"strings"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	nfs "github.com/willscott/go-nfs"
)

func onGetAttr(s *Server, cs *connState, call rpcCallHeader) []byte {
	rawFH, err := readFileHandle(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureGetAttr, nfs.NFSStatusInval)
	}
	rec, exp, err := resolveForAttributes(s, cs, call.Cred, rawFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureGetAttr, MapErrorToNFSStatus(err))
	}
	meta, err := statRecord(s, rec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureGetAttr, MapErrorToNFSStatus(err))
	}

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	_ = nfs.WritePostOpAttrs(&body, nodeMetaToFileAttr(&meta, exp.cap.AllowedUID, exp.cap.AllowedGID))
	raw := body.Bytes()
	var out bytes.Buffer
	writeUint32(&out, uint32(nfs.NFSStatusOk))
	_, _ = out.Write(raw[8:])
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, out.Bytes())
}

func onSetAttr(s *Server, cs *connState, call rpcCallHeader) []byte {
	rawFH, err := readFileHandle(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureSetAttr, nfs.NFSStatusInval)
	}
	attrs, err := readSattr3(call.Body, time.Now())
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureSetAttr, nfs.NFSStatusInval)
	}
	checkGuard, err := readBool(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureSetAttr, nfs.NFSStatusInval)
	}
	var guardSec, guardNsec uint32
	if checkGuard {
		if guardSec, err = readUint32(call.Body); err != nil {
			return encodeNFSError(call.XID, nfs.NFSProcedureSetAttr, nfs.NFSStatusInval)
		}
		if guardNsec, err = readUint32(call.Body); err != nil {
			return encodeNFSError(call.XID, nfs.NFSProcedureSetAttr, nfs.NFSStatusInval)
		}
	}
	rec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, rawFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureSetAttr, MapErrorToNFSStatus(err))
	}
	preMeta, err := statRecord(s, rec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureSetAttr, MapErrorToNFSStatus(err))
	}
	if checkGuard {
		if ct := nfs.ToNFSTime(preMeta.CTime); ct.Seconds != guardSec || ct.Nseconds != guardNsec {
			return encodeWccError(call.XID, nfs.NFSStatusNotSync, &preMeta, &preMeta, exp)
		}
	}
	if err := applySattr3(s, rec, exp, preMeta, attrs); err != nil {
		postMeta, _ := statRecord(s, rec)
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preMeta, &postMeta, exp)
	}
	postMeta, err := statRecord(s, rec)
	if err != nil {
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preMeta, nil, exp)
	}
	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writeWccData(&body, &preMeta, &postMeta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func applySattr3(s *Server, rec *handleRecord, exp *exportEntry, preMeta layeredfs.EntryAttr, attrs sattr3) error {
	if exp.cap.ReadOnly {
		return ErrReadOnlyExport
	}
	if (attrs.HasUID && attrs.UID != exp.cap.AllowedUID && attrs.UID != 0xFFFFFFFF) ||
		(attrs.HasGID && attrs.GID != exp.cap.AllowedGID && attrs.GID != 0xFFFFFFFF) {
		return syscall.EPERM
	}
	if attrs.HasSize && preMeta.Mode.IsDir() {
		return syscall.EISDIR
	}
	// Acknowledged UNSTABLE bytes must land before the attributes they precede: a truncate clips
	// them, a set mtime is not overwritten by their later arrival.
	if err := s.stability.FlushKey(exp.core, s.handles, s.cfg.FaultInjector, rec.key); err != nil {
		return err
	}
	defer s.stability.holdSidecarFlush(rec)()
	if err := s.stability.sidecars.materialize(exp.core, rec.qpath); err != nil {
		return err
	}
	req := layeredfs.SetAttrRequest{NoFollow: true}
	if attrs.HasSize {
		size := int64(attrs.Size)
		req.Size = &size
	}
	// A symlink's mode grants nothing, and tar -p sets one on every link it extracts.
	if attrs.HasMode && preMeta.Kind != layeredfs.EntrySymlink {
		req.Mode = &attrs.Mode
	}
	if attrs.HasAtime || attrs.HasMtime {
		atime, mtime := preMeta.ATime, preMeta.ModTime
		if attrs.HasAtime {
			atime = attrs.Atime
		}
		if attrs.HasMtime {
			mtime = attrs.Mtime
		}
		req.ATime, req.MTime = &atime, &mtime
	}
	if req.Size == nil && req.Mode == nil && req.ATime == nil {
		return nil
	}
	if rec.unlinked != nil {
		if rec.unlinked.meta.Layer != layeredfs.LayerPrivate {
			return ErrStaleHandle
		}
		_, err := exp.core.SetAttrNode(rec.unlinked.meta.NodeID, rec.unlinked.meta.Generation, req)
		return err
	}
	if _, err := exp.core.SetAttr(rec.qpath, req); err != nil {
		return err
	}
	s.stability.absorbFlushed(exp.core, rec)
	return nil
}

func onLookup(s *Server, cs *connState, call rpcCallHeader) []byte {
	dirFH, name, err := readDirOpArg(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureLookup, nfs.NFSStatusInval)
	}
	dirRec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, dirFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureLookup, MapErrorToNFSStatus(err))
	}
	dirMeta, err := statRecord(s, dirRec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureLookup, MapErrorToNFSStatus(err))
	}
	if !dirMeta.Mode.IsDir() {
		return encodePostOpError(call.XID, nfs.NFSStatusNotDir, &dirMeta, exp)
	}
	var childQPath layeredfs.QualifiedPath
	switch name {
	case ".":
		childQPath = dirRec.qpath
	case "..":
		childQPath = dirRec.qpath
		if idx := strings.LastIndexByte(dirRec.qpath.Rel, '/'); idx >= 0 {
			childQPath.Rel = dirRec.qpath.Rel[:idx]
		} else {
			childQPath.Rel = "."
		}
	default:
		if childQPath, err = joinChildPath(dirRec.qpath, name); err != nil {
			return encodePostOpError(call.XID, MapErrorToNFSStatus(err), &dirMeta, exp)
		}
	}
	defer s.stability.sidecars.settled(childQPath)()
	childMeta, err := exp.core.Lstat(childQPath)
	if err != nil {
		if adMeta, _, ok := s.stability.sidecars.synth(exp.core, childQPath); ok {
			childMeta = adMeta
		} else {
			return encodePostOpError(call.XID, MapErrorToNFSStatus(err), &dirMeta, exp)
		}
	}
	if s.cfg.FaultInjector != nil && s.cfg.FaultInjector.OnLookup != nil {
		if err := s.cfg.FaultInjector.OnLookup(dirRec.viewID, childQPath); err != nil {
			return encodePostOpError(call.XID, MapErrorToNFSStatus(err), &dirMeta, exp)
		}
	}
	childFH, ok := s.handles.Ensure(dirRec.key.viewHash, dirRec.viewID, childQPath, childMeta, dirRec.key.nodeID, cleanRel(childQPath.Rel) == ".")
	if !ok {
		return encodeNFSError(call.XID, nfs.NFSProcedureLookup, nfs.NFSStatusStale)
	}
	childMeta = wireMeta(s, dirRec.key.viewHash, childMeta)

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writeOpaque(&body, childFH[:])
	writePostOpAttr(&body, &childMeta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	writePostOpAttr(&body, &dirMeta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func onAccess(s *Server, cs *connState, call rpcCallHeader) []byte {
	rawFH, err := readFileHandle(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureAccess, nfs.NFSStatusInval)
	}
	reqMask, err := readUint32(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureAccess, nfs.NFSStatusInval)
	}
	rec, exp, err := resolveBootstrapHandle(s, cs, call.Cred, rawFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureAccess, MapErrorToNFSStatus(err))
	}
	meta, err := statRecord(s, rec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureAccess, MapErrorToNFSStatus(err))
	}
	var granted uint32
	if meta.Mode&0o400 != 0 {
		granted |= 0x0001 // READ
	}
	if meta.Mode.IsDir() {
		if meta.Mode&0o100 != 0 {
			granted |= 0x0002 // LOOKUP
		}
		if meta.Mode&0o200 != 0 {
			granted |= 0x0004 | 0x0008 | 0x0010
		}
	} else {
		if meta.Mode&0o200 != 0 {
			granted |= 0x0004 | 0x0008
		}
		if meta.Mode&0o100 != 0 {
			granted |= 0x0020 // EXECUTE
		}
	}
	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writePostOpAttr(&body, &meta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	writeUint32(&body, granted&reqMask)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func onReadlink(s *Server, cs *connState, call rpcCallHeader) []byte {
	rawFH, err := readFileHandle(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadlink, nfs.NFSStatusInval)
	}
	rec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, rawFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadlink, MapErrorToNFSStatus(err))
	}
	meta, err := statRecord(s, rec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadlink, MapErrorToNFSStatus(err))
	}
	target, err := exp.core.Readlink(rec.qpath)
	if err != nil {
		return encodePostOpError(call.XID, MapErrorToNFSStatus(err), &meta, exp)
	}
	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writePostOpAttr(&body, &meta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	writeString(&body, target)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func snapshotFlushLocked(e *stabilityEngine, hk handleKey) ([]unstableChunk, uint64, [8]byte) {
	var chunks []unstableChunk
	var flushedSeq uint64
	if st, ok := e.pending[hk]; ok && len(st.chunks) > 0 {
		chunks = append([]unstableChunk(nil), st.chunks...)
		for _, ch := range chunks {
			if ch.seq > flushedSeq {
				flushedSeq = ch.seq
			}
		}
		st.flushingSeq = flushedSeq
	}
	return chunks, flushedSeq, e.verf
}

func finishFlushLocked(e *stabilityEngine, hk handleKey, flushedSeq uint64, flushErr error) [8]byte {
	st, ok := e.pending[hk]
	if !ok {
		return e.verf
	}
	st.flushingSeq = 0
	if flushErr != nil {
		st.chunks = coalesceAdjacentChunks(st.chunks, 0)
		return e.verf
	}
	kept := st.chunks[:0]
	var maxEnd uint64
	for _, ch := range st.chunks {
		if ch.seq > flushedSeq {
			kept = append(kept, ch)
			if end := ch.offset + uint64(len(ch.data)); end > maxEnd {
				maxEnd = end
			}
		}
	}
	st.chunks = coalesceAdjacentChunks(kept, 0)
	st.maxEnd = maxEnd
	if len(st.chunks) == 0 {
		delete(e.pending, hk)
	}
	return e.verf
}
