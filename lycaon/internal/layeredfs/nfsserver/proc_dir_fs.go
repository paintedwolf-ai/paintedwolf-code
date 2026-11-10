package nfsserver

import (
	"bytes"

	"github.com/lycaon/lycaon/internal/layeredfs"
	nfs "github.com/willscott/go-nfs"
)

const (
	fsf3Link        = 0x0001
	fsf3Symlink     = 0x0002
	fsf3Homogeneous = 0x0008
	fsf3CanSetTime  = 0x0010
	pathconfLinkMax = 32767
)

func entryFileID(core *layeredfs.Core, dirMeta layeredfs.EntryAttr, ent dirSnapshotEntry) layeredfs.NodeID {
	if ent.StableID != 0 {
		return ent.StableID
	}
	if meta, err := core.Lstat(ent.QPath); err == nil {
		return meta.StableID()
	}
	return dirMeta.NodeID
}

func onReaddir(s *Server, cs *connState, call rpcCallHeader) []byte {
	dirFH, err := readFileHandle(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadDir, nfs.NFSStatusInval)
	}
	cookie, err := readUint64(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadDir, nfs.NFSStatusInval)
	}
	verfBytes, err := readFixedBytes(call.Body, 8)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadDir, nfs.NFSStatusInval)
	}
	count, err := readUint32(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadDir, nfs.NFSStatusInval)
	}
	var clientVerf [8]byte
	copy(clientVerf[:], verfBytes)

	dirRec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, dirFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadDir, MapErrorToNFSStatus(err))
	}
	dirMeta, err := statRecord(s, dirRec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadDir, MapErrorToNFSStatus(err))
	}
	if !dirMeta.Mode.IsDir() {
		return encodePostOpError(call.XID, nfs.NFSStatusNotDir, &dirMeta, exp)
	}
	if count < 128 {
		return encodePostOpError(call.XID, nfs.NFSStatusTooSmall, &dirMeta, exp)
	}
	if count > maxDirCookieBytes {
		count = maxDirCookieBytes
	}

	entries, activeVerf, err := s.dirs.ReadSnapshot(exp.core, dirRec, cookie, clientVerf)
	if err != nil {
		return encodePostOpError(call.XID, MapErrorToNFSStatus(err), &dirMeta, exp)
	}

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writePostOpAttr(&body, &dirMeta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	_, _ = body.Write(activeVerf[:])

	eof := true
	written := 0
	for _, ent := range entries {
		var entBuf bytes.Buffer
		writeUint32(&entBuf, 1)
		writeUint64(&entBuf, uint64(entryFileID(exp.core, dirMeta, ent)))
		writeString(&entBuf, ent.Name)
		writeUint64(&entBuf, ent.Cookie)
		if body.Len()+entBuf.Len()+8 > int(count) {
			if written == 0 {
				return encodePostOpError(call.XID, nfs.NFSStatusTooSmall, &dirMeta, exp)
			}
			eof = false
			break
		}
		_, _ = body.Write(entBuf.Bytes())
		written++
	}
	writeUint32(&body, 0)
	writeBool(&body, eof)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func onReaddirPlus(s *Server, cs *connState, call rpcCallHeader) []byte {
	dirFH, err := readFileHandle(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadDirPlus, nfs.NFSStatusInval)
	}
	cookie, err := readUint64(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadDirPlus, nfs.NFSStatusInval)
	}
	verfBytes, err := readFixedBytes(call.Body, 8)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadDirPlus, nfs.NFSStatusInval)
	}
	dirCount, err := readUint32(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadDirPlus, nfs.NFSStatusInval)
	}
	maxCount, err := readUint32(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadDirPlus, nfs.NFSStatusInval)
	}
	var clientVerf [8]byte
	copy(clientVerf[:], verfBytes)

	dirRec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, dirFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadDirPlus, MapErrorToNFSStatus(err))
	}
	dirMeta, err := statRecord(s, dirRec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureReadDirPlus, MapErrorToNFSStatus(err))
	}
	if !dirMeta.Mode.IsDir() {
		return encodePostOpError(call.XID, nfs.NFSStatusNotDir, &dirMeta, exp)
	}
	if maxCount < 256 || dirCount < 32 {
		return encodePostOpError(call.XID, nfs.NFSStatusTooSmall, &dirMeta, exp)
	}
	if maxCount > maxDirCookieBytes {
		maxCount = maxDirCookieBytes
	}

	entries, activeVerf, err := s.dirs.ReadSnapshot(exp.core, dirRec, cookie, clientVerf)
	if err != nil {
		return encodePostOpError(call.XID, MapErrorToNFSStatus(err), &dirMeta, exp)
	}

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writePostOpAttr(&body, &dirMeta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	_, _ = body.Write(activeVerf[:])

	eof := true
	written := 0
	var usedDirBytes uint32
	for _, ent := range entries {
		var entBuf bytes.Buffer
		writeUint32(&entBuf, 1)
		writeUint64(&entBuf, uint64(entryFileID(exp.core, dirMeta, ent)))
		writeString(&entBuf, ent.Name)
		writeUint64(&entBuf, ent.Cookie)
		dirStep := uint32(entBuf.Len())
		// Attributes come from the object as it is now. An entry that has since gone still takes its
		// place in the listing, without attributes or a handle, as RFC 1813 permits.
		if meta, statErr := exp.core.Lstat(ent.QPath); statErr == nil {
			fh, ok := s.handles.Ensure(dirRec.key.viewHash, dirRec.viewID, ent.QPath, meta, dirRec.key.nodeID, cleanRel(ent.QPath.Rel) == ".")
			if !ok {
				return encodeNFSError(call.XID, nfs.NFSProcedureReadDirPlus, nfs.NFSStatusStale)
			}
			meta = wireMeta(s, dirRec.key.viewHash, meta)
			writePostOpAttr(&entBuf, &meta, exp.cap.AllowedUID, exp.cap.AllowedGID)
			writePostOpFH3(&entBuf, fh[:])
		} else {
			writePostOpAttr(&entBuf, nil, exp.cap.AllowedUID, exp.cap.AllowedGID)
			writePostOpFH3(&entBuf, nil)
		}

		if body.Len()+entBuf.Len()+8 > int(maxCount) || usedDirBytes+dirStep > dirCount {
			if written == 0 {
				return encodePostOpError(call.XID, nfs.NFSStatusTooSmall, &dirMeta, exp)
			}
			eof = false
			break
		}
		usedDirBytes += dirStep
		_, _ = body.Write(entBuf.Bytes())
		written++
	}
	writeUint32(&body, 0)
	writeBool(&body, eof)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func onFSStat(s *Server, cs *connState, call rpcCallHeader) []byte {
	rawFH, err := readFileHandle(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureFSStat, nfs.NFSStatusInval)
	}
	rec, exp, err := resolveBootstrapHandle(s, cs, call.Cred, rawFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureFSStat, MapErrorToNFSStatus(err))
	}
	meta, err := statRecord(s, rec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureFSStat, MapErrorToNFSStatus(err))
	}

	capacity, err := exp.core.BackingCapacity()
	if err != nil {
		return encodePostOpError(call.XID, MapErrorToNFSStatus(err), &meta, exp)
	}
	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writePostOpAttr(&body, &meta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	for _, v := range []uint64{capacity.TotalBytes, capacity.FreeBytes, capacity.AvailableBytes, capacity.TotalFiles, capacity.FreeFiles, capacity.FreeFiles} {
		writeUint64(&body, v)
	}
	writeUint32(&body, 0)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func onFSInfo(s *Server, cs *connState, call rpcCallHeader) []byte {
	rawFH, err := readFileHandle(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureFSInfo, nfs.NFSStatusInval)
	}
	rec, exp, err := resolveBootstrapHandle(s, cs, call.Cred, rawFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureFSInfo, MapErrorToNFSStatus(err))
	}
	meta, err := statRecord(s, rec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureFSInfo, MapErrorToNFSStatus(err))
	}

	maxTransfer := uint32(min(s.cfg.MaxRequestBytes, 64<<10))
	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writePostOpAttr(&body, &meta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	for _, v := range []uint32{maxTransfer, maxTransfer, 4096, maxTransfer, maxTransfer, 4096, maxTransfer} {
		writeUint32(&body, v)
	}
	writeUint64(&body, 1<<40)
	writeUint32(&body, 0)
	writeUint32(&body, 1)
	writeUint32(&body, fsf3Link|fsf3Symlink|fsf3Homogeneous|fsf3CanSetTime)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func onPathConf(s *Server, cs *connState, call rpcCallHeader) []byte {
	rawFH, err := readFileHandle(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedurePathConf, nfs.NFSStatusInval)
	}
	rec, exp, err := resolveBootstrapHandle(s, cs, call.Cred, rawFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedurePathConf, MapErrorToNFSStatus(err))
	}
	meta, err := statRecord(s, rec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedurePathConf, MapErrorToNFSStatus(err))
	}

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writePostOpAttr(&body, &meta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	writeUint32(&body, pathconfLinkMax)
	writeUint32(&body, nfs.MNTNameLen)
	writeBool(&body, true)
	writeBool(&body, true)
	writeBool(&body, false)
	writeBool(&body, true)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func chunksByteCount(chunks []unstableChunk) int {
	total := 0
	for _, ch := range chunks {
		total += len(ch.data)
	}
	return total
}

func pendingStatsLocked(e *stabilityEngine, vHash uint64) (viewBytes, viewChunks, totalBytes int) {
	for hk, st := range e.pending {
		b := chunksByteCount(st.chunks)
		totalBytes += b
		if hk.viewHash == vHash {
			viewBytes += b
			viewChunks += len(st.chunks)
		}
	}
	return viewBytes, viewChunks, totalBytes
}

func flushPendingKey(
	e *stabilityEngine, core *layeredfs.Core, handles *handleTable, fault *FaultInjector, hk handleKey,
) error {
	acquireNodeFlush(e, hk)
	defer releaseNodeFlush(e, hk)
	e.mu.Lock()
	st, ok := e.pending[hk]
	var wire [HandleSize]byte
	if ok {
		wire = st.rec.wire
	}
	chunks, flushedSeq, _ := snapshotFlushLocked(e, hk)
	e.mu.Unlock()
	if len(chunks) == 0 {
		return nil
	}
	// The bytes belong to the object, wherever it now lives. If it was replaced or removed they have
	// nowhere to go, and they must never reach whatever now holds its old name.
	rec, err := handles.Resolve(wire[:], 0)
	if err != nil {
		e.DiscardKey(hk)
		return nil
	}
	_, err = flushChunksToCore(core, handles, e.sidecars, fault, rec, chunks)
	e.mu.Lock()
	_ = finishFlushLocked(e, hk, flushedSeq, err)
	e.mu.Unlock()
	if err == nil {
		e.absorbFlushed(core, rec)
	}
	return err
}
