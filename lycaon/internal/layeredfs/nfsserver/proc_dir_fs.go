package nfsserver

import (
	"bytes"

	nfs "github.com/willscott/go-nfs"
)

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

	entries, activeVerf, err := s.dirs.ReadSnapshot(exp.core, dirRec, dirMeta, cookie, clientVerf)
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
		writeUint64(&entBuf, uint64(ent.Meta.NodeID))
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

	entries, activeVerf, err := s.dirs.ReadSnapshot(exp.core, dirRec, dirMeta, cookie, clientVerf)
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
		fh := s.handles.Ensure(dirRec.viewID, ent.QPath, ent.Meta, cleanRel(ent.QPath.Rel) == ".", false)
		meta := ent.Meta
		if rec, ok := s.handles.LookupByPath(dirRec.viewID, ent.QPath); ok {
			meta = s.stability.EffectiveMeta(rec.key, meta)
		}

		var entBuf bytes.Buffer
		writeUint32(&entBuf, 1)
		writeUint64(&entBuf, uint64(meta.NodeID))
		writeString(&entBuf, ent.Name)
		writeUint64(&entBuf, ent.Cookie)
		dirStep := uint32(entBuf.Len())
		writePostOpAttr(&entBuf, &meta, exp.cap.AllowedUID, exp.cap.AllowedGID)
		writePostOpFH3(&entBuf, fh[:])

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
	rec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, rawFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureFSStat, MapErrorToNFSStatus(err))
	}
	meta, err := statRecord(s, rec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureFSStat, MapErrorToNFSStatus(err))
	}

	const (
		totalBytes uint64 = 64 << 30
		freeBytes  uint64 = 32 << 30
		totalFiles uint64 = 1 << 20
		freeFiles  uint64 = 1 << 19
	)
	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writePostOpAttr(&body, &meta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	for _, v := range []uint64{totalBytes, freeBytes, freeBytes, totalFiles, freeFiles, freeFiles} {
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
	rec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, rawFH)
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
	writeUint32(&body, 0x0002|0x0008|0x0010)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func onPathConf(s *Server, cs *connState, call rpcCallHeader) []byte {
	rawFH, err := readFileHandle(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedurePathConf, nfs.NFSStatusInval)
	}
	rec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, rawFH)
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
	writeUint32(&body, 1)
	writeUint32(&body, nfs.MNTNameLen)
	writeBool(&body, true)
	writeBool(&body, true)
	writeBool(&body, false)
	writeBool(&body, true)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}
