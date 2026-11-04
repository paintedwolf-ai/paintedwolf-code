package nfsserver

import (
	"bytes"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/layeredfs"
	nfs "github.com/willscott/go-nfs"
)

func statRecord(s *Server, rec *handleRecord) (layeredfs.EntryAttr, error) {
	viewCore := coreForView(s, rec.viewID)
	if rec.unlinked != nil {
		if rec.unlinked.fd != 0 {
			meta, err := viewCore.StatHandle(rec.unlinked.fd)
			if err == nil {
				meta = s.stability.EffectiveMeta(rec.key, meta)
				s.handles.UpdateUnlinkedMeta(rec.key, meta)
				return meta, nil
			}
		}
		return s.stability.EffectiveMeta(rec.key, rec.unlinked.meta), nil
	}
	meta, err := viewCore.Lstat(rec.qpath)
	if err != nil {
		adMeta, _, ok := synthAppleDoubleAttr(viewCore, rec.qpath)
		if !ok {
			return layeredfs.EntryAttr{}, err
		}
		meta = adMeta
	}
	// The handle names an object, not a path: another object now at the name is not this one.
	if meta.StableID() != rec.key.nodeID {
		return layeredfs.EntryAttr{}, ErrStaleHandle
	}
	return wireMeta(s, rec.key.viewHash, meta), nil
}

// wireMeta presents attributes as the client should see them: the stable identity as fileid, and
// sizes including UNSTABLE bytes acknowledged but not yet flushed.
func wireMeta(s *Server, viewHash uint64, meta layeredfs.EntryAttr) layeredfs.EntryAttr {
	id := meta.StableID()
	meta = s.stability.EffectiveMeta(handleKey{viewHash: viewHash, nodeID: id}, meta)
	meta.NodeID = id
	return meta
}

func onRead(s *Server, cs *connState, call rpcCallHeader) []byte {
	rawFH, err := readFileHandle(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRead, nfs.NFSStatusInval)
	}
	offset, err := readUint64(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRead, nfs.NFSStatusInval)
	}
	count, err := readUint32(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRead, nfs.NFSStatusInval)
	}
	if count > uint32(s.cfg.MaxRequestBytes) {
		count = uint32(s.cfg.MaxRequestBytes)
	}

	rec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, rawFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRead, MapErrorToNFSStatus(err))
	}
	meta, err := statRecord(s, rec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRead, MapErrorToNFSStatus(err))
	}
	if meta.Mode.IsDir() {
		return encodePostOpError(call.XID, nfs.NFSStatusIsDir, &meta, exp)
	}
	if meta.Mode&os.ModeSymlink != 0 {
		return encodePostOpError(call.XID, nfs.NFSStatusInval, &meta, exp)
	}

	baseData, baseSize, err := readBaseFile(exp.core, rec, offset, int(count))
	if err != nil {
		return encodePostOpError(call.XID, MapErrorToNFSStatus(err), &meta, exp)
	}
	data, eof := s.stability.OverlayRead(rec.key, offset, count, baseData, baseSize)

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writePostOpAttr(&body, &meta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	writeUint32(&body, uint32(len(data)))
	writeBool(&body, eof)
	writeOpaque(&body, data)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func readBaseFile(viewCore *layeredfs.Core, rec *handleRecord, offset uint64, count int) ([]byte, int64, error) {
	if rec.unlinked != nil {
		if rec.unlinked.fd == 0 {
			return nil, 0, ErrStaleHandle
		}
		meta, err := viewCore.StatHandle(rec.unlinked.fd)
		if err != nil {
			return nil, 0, err
		}
		buf := make([]byte, max(count, 0))
		n, rErr := viewCore.ReadHandle(rec.unlinked.fd, buf, int64(offset))
		if rErr != nil && rErr != io.EOF {
			return nil, 0, rErr
		}
		return buf[:n], meta.Size, nil
	}
	fd, meta, err := viewCore.Open(rec.qpath, layeredfs.OpenReadOnly, 0)
	if err != nil {
		if adMeta, payload, ok := synthAppleDoubleAttr(viewCore, rec.qpath); ok {
			if offset >= uint64(len(payload)) {
				return nil, adMeta.Size, nil
			}
			end := min(int(offset)+max(count, 0), len(payload))
			return payload[offset:end], adMeta.Size, nil
		}
		return nil, 0, err
	}
	if meta.StableID() != rec.key.nodeID {
		_ = viewCore.CloseHandle(fd)
		return nil, 0, ErrStaleHandle
	}
	buf := make([]byte, max(count, 0))
	n, rErr := viewCore.ReadHandle(fd, buf, int64(offset))
	_ = viewCore.CloseHandle(fd)
	if rErr != nil && rErr != io.EOF {
		return nil, 0, rErr
	}
	return buf[:n], meta.Size, nil
}

func onWrite(s *Server, cs *connState, call rpcCallHeader) []byte {
	rawFH, err := readFileHandle(call.Body)
	if err != nil { return encodeNFSError(call.XID, nfs.NFSProcedureWrite, nfs.NFSStatusInval) }
	offset, err := readUint64(call.Body)
	if err != nil { return encodeNFSError(call.XID, nfs.NFSProcedureWrite, nfs.NFSStatusInval) }
	count, err := readUint32(call.Body)
	if err != nil { return encodeNFSError(call.XID, nfs.NFSProcedureWrite, nfs.NFSStatusInval) }
	stableRaw, err := readUint32(call.Body)
	if err != nil || stableRaw > uint32(FileSync) { return encodeNFSError(call.XID, nfs.NFSProcedureWrite, nfs.NFSStatusInval) }
	data, err := readOpaqueBounded(call.Body, s.cfg.MaxRequestBytes)
	if err != nil { return encodeNFSError(call.XID, nfs.NFSProcedureWrite, nfs.NFSStatusInval) }
	if uint32(len(data)) > count { data = data[:count] }
	stability := WriteStability(stableRaw)

	rec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, rawFH)
	if err != nil { return encodeNFSError(call.XID, nfs.NFSProcedureWrite, MapErrorToNFSStatus(err)) }
	preMeta, err := statRecord(s, rec)
	if err != nil { return encodeNFSError(call.XID, nfs.NFSProcedureWrite, MapErrorToNFSStatus(err)) }
	if exp.cap.ReadOnly {
		return encodeWccError(call.XID, nfs.NFSStatusROFS, &preMeta, &preMeta, exp)
	}
	if preMeta.Mode.IsDir() {
		return encodeWccError(call.XID, nfs.NFSStatusIsDir, &preMeta, &preMeta, exp)
	}
	if preMeta.Mode&os.ModeSymlink != 0 {
		return encodeWccError(call.XID, nfs.NFSStatusInval, &preMeta, &preMeta, exp)
	}
	if err := materializeSidecar(exp.core, rec.qpath); err != nil {
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preMeta, &preMeta, exp)
	}
	// Permission to write was decided when the client opened the file, and a descriptor keeps it
	// through a later chmod, so WRITE follows export authorization and never the current mode.
	if rec.unlinked == nil && preMeta.Layer != layeredfs.LayerPrivate {
		if err := ensureWritableCopy(exp.core, rec); err != nil {
			return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preMeta, &preMeta, exp)
		}
	}
	if s.cfg.FaultInjector != nil && s.cfg.FaultInjector.OnWrite != nil {
		if err := s.cfg.FaultInjector.OnWrite(rec.viewID, rec.qpath, offset, len(data), stability); err != nil {
			return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preMeta, &preMeta, exp)
		}
	}

	var (
		postMeta  layeredfs.EntryAttr
		verf      [8]byte
		committed = stability
	)
	if stability == Unstable {
		postMeta, committed, verf, err = s.stability.StageUnstable(
			exp.core, s.handles, s.cfg.FaultInjector, rec, preMeta, offset, data,
		)
	} else {
		postMeta, verf, err = s.stability.WriteStable(
			exp.core, s.handles, s.cfg.FaultInjector, rec, offset, data, stability,
		)
	}
	if err != nil {
		curMeta, _ := statRecord(s, rec)
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preMeta, &curMeta, exp)
	}

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writeWccData(&body, &preMeta, &postMeta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	writeUint32(&body, uint32(len(data)))
	writeUint32(&body, uint32(committed))
	_, _ = body.Write(verf[:])
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

// ensureWritableCopy copies an untouched object up before its first WRITE is acknowledged, so a
// copy-up failure reaches the client instead of being discovered at COMMIT.
func ensureWritableCopy(viewCore *layeredfs.Core, rec *handleRecord) error {
	fd, meta, err := viewCore.Open(rec.qpath, layeredfs.OpenReadWrite, 0)
	if err != nil {
		return err
	}
	_ = viewCore.CloseHandle(fd)
	if meta.StableID() != rec.key.nodeID {
		return ErrStaleHandle
	}
	return nil
}

func onCommit(s *Server, cs *connState, call rpcCallHeader) []byte {
	rawFH, err := readFileHandle(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureCommit, nfs.NFSStatusInval)
	}
	offset, err := readUint64(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureCommit, nfs.NFSStatusInval)
	}
	count, err := readUint32(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureCommit, nfs.NFSStatusInval)
	}

	rec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, rawFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureCommit, MapErrorToNFSStatus(err))
	}
	preMeta, err := statRecord(s, rec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureCommit, MapErrorToNFSStatus(err))
	}
	if exp.cap.ReadOnly {
		return encodeWccError(call.XID, nfs.NFSStatusROFS, &preMeta, &preMeta, exp)
	}
	postMeta, verf, err := s.stability.Commit(
		exp.core, s.handles, s.cfg.FaultInjector, rec, preMeta, offset, count,
	)
	if err != nil {
		curMeta, _ := statRecord(s, rec)
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preMeta, &curMeta, exp)
	}

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writeWccData(&body, &preMeta, &postMeta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	_, _ = body.Write(verf[:])
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func acquireNodeFlush(e *stabilityEngine, hk handleKey) {
	e.mu.Lock()
	for {
		ch, busy := e.flushing[hk]
		if !busy {
			e.flushing[hk] = make(chan struct{})
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()
		<-ch
		e.mu.Lock()
	}
}

func releaseNodeFlush(e *stabilityEngine, hk handleKey) {
	e.mu.Lock()
	if ch, ok := e.flushing[hk]; ok {
		delete(e.flushing, hk)
		close(ch)
	}
	e.mu.Unlock()
}
