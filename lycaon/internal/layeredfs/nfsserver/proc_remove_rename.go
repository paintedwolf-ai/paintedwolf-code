package nfsserver

import (
	"bytes"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
	nfs "github.com/willscott/go-nfs"
)

func onRemove(s *Server, cs *connState, call rpcCallHeader) []byte {
	dirFH, name, err := readDirOpArg(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRemove, nfs.NFSStatusInval)
	}
	dirRec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, dirFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRemove, MapErrorToNFSStatus(err))
	}
	preDir, err := statRecord(s, dirRec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRemove, MapErrorToNFSStatus(err))
	}
	if exp.cap.ReadOnly {
		return encodeWccError(call.XID, nfs.NFSStatusROFS, &preDir, &preDir, exp)
	}
	childQPath, err := joinChildPath(dirRec.qpath, name)
	if err != nil {
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preDir, &preDir, exp)
	}

	err = executeRemove(s, exp.core, dirRec, childQPath)
	postDir, _ := statRecord(s, dirRec)
	if err != nil {
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preDir, &postDir, exp)
	}
	s.dirs.InvalidateDir(dirRec.key)

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writeWccData(&body, &preDir, &postDir, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func executeRemove(s *Server, viewCore *layeredfs.Core, dirRec *handleRecord, childQPath layeredfs.QualifiedPath) error {
	childRec, hasHandle := s.handles.LookupByPath(dirRec.viewID, childQPath)
	var openFD layeredfs.HandleID
	var childMeta layeredfs.EntryAttr
	if hasHandle {
		meta, err := viewCore.Lstat(childQPath)
		if err == nil && meta.Mode.IsRegular() {
			_, _, _ = s.stability.Commit(viewCore, s.handles, nil, childRec, meta, 0, 0)
			if meta.Mode&0o200 == 0 && childRec.ownerCreatedRO {
				_, _ = viewCore.Chmod(childQPath, (meta.Mode&os.ModePerm)|0o200)
			}
			if fd, _, openErr := viewCore.Open(childQPath, layeredfs.OpenReadWrite, 0); openErr == nil {
				openFD, childMeta = fd, meta
			} else if fd, _, openErr := viewCore.Open(childQPath, layeredfs.OpenReadOnly, 0); openErr == nil {
				openFD, childMeta = fd, meta
			}
		}
	}

	if err := viewCore.Unlink(childQPath, false); err != nil {
		if openFD != 0 {
			_ = viewCore.CloseHandle(openFD)
		}
		return err
	}
	if openFD != 0 {
		s.handles.MarkUnlinked(dirRec.viewID, childQPath, openFD, childMeta)
	} else {
		s.handles.InvalidatePath(viewCore, dirRec.viewID, childQPath)
	}
	return nil
}

func onRmdir(s *Server, cs *connState, call rpcCallHeader) []byte {
	dirFH, name, err := readDirOpArg(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRmDir, nfs.NFSStatusInval)
	}
	dirRec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, dirFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRmDir, MapErrorToNFSStatus(err))
	}
	preDir, err := statRecord(s, dirRec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRmDir, MapErrorToNFSStatus(err))
	}
	if exp.cap.ReadOnly {
		return encodeWccError(call.XID, nfs.NFSStatusROFS, &preDir, &preDir, exp)
	}
	childQPath, err := joinChildPath(dirRec.qpath, name)
	if err != nil {
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preDir, &preDir, exp)
	}

	err = exp.core.Unlink(childQPath, true)
	postDir, _ := statRecord(s, dirRec)
	if err != nil {
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preDir, &postDir, exp)
	}
	s.handles.InvalidatePath(exp.core, dirRec.viewID, childQPath)
	s.dirs.InvalidateDir(dirRec.key)

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writeWccData(&body, &preDir, &postDir, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func onRename(s *Server, cs *connState, call rpcCallHeader) []byte {
	fromDirFH, fromName, err := readDirOpArg(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRename, nfs.NFSStatusInval)
	}
	toDirFH, toName, err := readDirOpArg(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRename, nfs.NFSStatusInval)
	}

	fromDirRec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, fromDirFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRename, MapErrorToNFSStatus(err))
	}
	toDirRec, _, err := resolveAndAuthHandle(s, cs, call.Cred, toDirFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRename, MapErrorToNFSStatus(err))
	}
	if fromDirRec.viewID != toDirRec.viewID {
		return encodeNFSError(call.XID, nfs.NFSProcedureRename, nfs.NFSStatusAccess)
	}

	preFrom, err := statRecord(s, fromDirRec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRename, MapErrorToNFSStatus(err))
	}
	preTo, err := statRecord(s, toDirRec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureRename, MapErrorToNFSStatus(err))
	}
	if exp.cap.ReadOnly {
		return encodeRenameError(call.XID, nfs.NFSStatusROFS, &preFrom, &preFrom, &preTo, &preTo, exp)
	}

	fromQPath, err := joinChildPath(fromDirRec.qpath, fromName)
	if err != nil {
		return encodeRenameError(call.XID, MapErrorToNFSStatus(err), &preFrom, &preFrom, &preTo, &preTo, exp)
	}
	toQPath, err := joinChildPath(toDirRec.qpath, toName)
	if err != nil {
		return encodeRenameError(call.XID, MapErrorToNFSStatus(err), &preFrom, &preFrom, &preTo, &preTo, exp)
	}

	if srcRec, ok := s.handles.LookupByPath(fromDirRec.viewID, fromQPath); ok {
		if srcMeta, statErr := statRecord(s, srcRec); statErr == nil {
			_, _, _ = s.stability.Commit(exp.core, s.handles, nil, srcRec, srcMeta, 0, 0)
		}
	}

	err = exp.core.Rename(fromQPath, toQPath)
	postFrom, _ := statRecord(s, fromDirRec)
	postTo, _ := statRecord(s, toDirRec)
	if err != nil {
		return encodeRenameError(call.XID, MapErrorToNFSStatus(err), &preFrom, &postFrom, &preTo, &postTo, exp)
	}

	s.handles.UpdateOnRename(exp.core, fromDirRec.viewID, fromQPath, toQPath)
	if movedRec, ok := s.handles.LookupByPath(fromDirRec.viewID, toQPath); ok {
		s.stability.UpdateRecPath(movedRec.key, toQPath)
	}
	syncAppleDoubleXattrsIfPresent(exp.core, toQPath)
	s.dirs.InvalidateDir(fromDirRec.key)
	if toDirRec.key != fromDirRec.key {
		s.dirs.InvalidateDir(toDirRec.key)
	}

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writeWccData(&body, &preFrom, &postFrom, exp.cap.AllowedUID, exp.cap.AllowedGID)
	writeWccData(&body, &preTo, &postTo, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func encodeRenameError(
	xid uint32, st nfs.NFSStatus, preFrom, postFrom, preTo, postTo *layeredfs.EntryAttr, exp *exportEntry,
) []byte {
	var body bytes.Buffer
	writeUint32(&body, uint32(st))
	writeWccData(&body, preFrom, postFrom, exp.cap.AllowedUID, exp.cap.AllowedGID)
	writeWccData(&body, preTo, postTo, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(xid, nfs.ResponseCodeSuccess, body.Bytes())
}

func onLink(s *Server, cs *connState, call rpcCallHeader) []byte {
	targetFH, err := readFileHandle(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureLink, nfs.NFSStatusInval)
	}
	dirFH, _, err := readDirOpArg(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureLink, nfs.NFSStatusInval)
	}
	targetRec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, targetFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureLink, MapErrorToNFSStatus(err))
	}
	dirRec, _, err := resolveAndAuthHandle(s, cs, call.Cred, dirFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureLink, MapErrorToNFSStatus(err))
	}
	if targetRec.viewID != dirRec.viewID {
		return encodeNFSError(call.XID, nfs.NFSProcedureLink, nfs.NFSStatusAccess)
	}
	targetMeta, _ := statRecord(s, targetRec)
	dirMeta, _ := statRecord(s, dirRec)

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusNotSupp))
	writePostOpAttr(&body, &targetMeta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	writeWccData(&body, &dirMeta, &dirMeta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func (t *handleTable) UpdateOnRename(core *layeredfs.Core, viewID ViewID, from, to layeredfs.QualifiedPath) {
	t.mu.Lock()
	from.Rel, to.Rel = cleanRel(from.Rel), cleanRel(to.Rel)
	fromPK, toPK := makePathKey(viewID, from), makePathKey(viewID, to)
	srcRec := t.byPath[fromPK]
	var toClose layeredfs.HandleID
	if dstRec, ok := t.byPath[toPK]; ok && dstRec != srcRec {
		delete(t.byPath, toPK)
		delete(t.byKey, dstRec.key)
		if dstRec.lruElem != nil {
			t.lru.Remove(dstRec.lruElem)
			dstRec.lruElem = nil
		}
		if dstRec.unlinked != nil {
			toClose = dstRec.unlinked.fd
			dstRec.unlinked.fd = 0
		}
	}
	prefix := from.Rel + "/"
	for pk, rec := range t.byPath {
		if pk.viewID != viewID || pk.root != from.RootID {
			continue
		}
		if pk.relPath == from.Rel {
			delete(t.byPath, pk)
			rec.qpath = to
			t.byPath[toPK] = rec
		} else if from.Rel != "." && strings.HasPrefix(pk.relPath, prefix) {
			delete(t.byPath, pk)
			suffix := strings.TrimPrefix(pk.relPath, prefix)
			newRel := suffix
			if to.Rel != "." {
				newRel = to.Rel + "/" + suffix
			}
			rec.qpath = layeredfs.QualifiedPath{RootID: to.RootID, Rel: newRel}
			t.byPath[makePathKey(viewID, rec.qpath)] = rec
		}
	}
	t.mu.Unlock()
	if core != nil && toClose != 0 {
		_ = core.CloseHandle(toClose)
	}
}

func (t *handleTable) MarkUnlinked(viewID ViewID, qpath layeredfs.QualifiedPath, openFD layeredfs.HandleID, meta layeredfs.EntryAttr) {
	t.mu.Lock()
	defer t.mu.Unlock()
	pk := makePathKey(viewID, qpath)
	if rec, ok := t.byPath[pk]; ok {
		delete(t.byPath, pk)
		rec.unlinked = &unlinkedFile{fd: openFD, meta: meta}
	}
}

func (t *handleTable) UpdateUnlinkedMeta(hk handleKey, meta layeredfs.EntryAttr) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if rec, ok := t.byKey[hk]; ok && rec.unlinked != nil {
		rec.unlinked.meta = meta
	}
}

func (t *handleTable) InvalidatePath(core *layeredfs.Core, viewID ViewID, qpath layeredfs.QualifiedPath) {
	t.mu.Lock()
	pk := makePathKey(viewID, qpath)
	rec, ok := t.byPath[pk]
	var toClose layeredfs.HandleID
	if ok {
		delete(t.byPath, pk)
		delete(t.byKey, rec.key)
		if rec.lruElem != nil {
			t.lru.Remove(rec.lruElem)
			rec.lruElem = nil
		}
		if rec.unlinked != nil {
			toClose = rec.unlinked.fd
			rec.unlinked.fd = 0
		}
	}
	t.mu.Unlock()
	if core != nil && toClose != 0 {
		_ = core.CloseHandle(toClose)
	}
}
