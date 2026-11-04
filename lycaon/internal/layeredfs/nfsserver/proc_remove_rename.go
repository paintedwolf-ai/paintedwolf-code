package nfsserver

import (
	"bytes"
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

// executeRemove keeps a removed object reachable for a held handle when it can do so cheaply: a
// private object through its backing file, an untouched one through a read descriptor. Nothing is
// copied up just to be deleted, and the held set is bounded, so rm -rf costs no per-file state.
func executeRemove(s *Server, viewCore *layeredfs.Core, dirRec *handleRecord, childQPath layeredfs.QualifiedPath) error {
	childRec, hasHandle := s.handles.LookupByPath(dirRec.viewID, childQPath)
	var openFD layeredfs.HandleID
	var childMeta layeredfs.EntryAttr
	if hasHandle {
		meta, err := viewCore.Lstat(childQPath)
		if err == nil && meta.Mode.IsRegular() && meta.Nlink <= 1 && len(childRec.paths) <= 1 {
			flags := layeredfs.OpenReadOnly
			if meta.Layer == layeredfs.LayerPrivate {
				flags = layeredfs.OpenReadWrite
			}
			if fd, opened, openErr := viewCore.Open(childQPath, flags, 0); openErr == nil {
				if opened.StableID() == childRec.key.nodeID {
					openFD, childMeta = fd, opened
				} else {
					_ = viewCore.CloseHandle(fd)
				}
			}
		}
	}
	if err := viewCore.Unlink(childQPath, false); err != nil {
		if openFD != 0 {
			_ = viewCore.CloseHandle(openFD)
		}
		if _, _, presented := s.stability.sidecars.synth(viewCore, childQPath); !presented {
			return err
		}
		s.stability.sidecars.clearPresented(viewCore, childQPath)
	}
	if openFD != 0 {
		closeCoreHandles(viewCore, s.handles.MarkUnlinked(dirRec.viewID, childQPath, openFD, childMeta))
		return nil
	}
	s.handles.InvalidatePath(viewCore, dirRec.viewID, childQPath)
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
	fromDirFH, fromName, err1 := readDirOpArg(call.Body)
	toDirFH, toName, err2 := readDirOpArg(call.Body)
	if err1 != nil || err2 != nil {
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
	preFrom, err1 := statRecord(s, fromDirRec)
	preTo, err2 := statRecord(s, toDirRec)
	if err1 != nil || err2 != nil {
		if err1 != nil {
			return encodeNFSError(call.XID, nfs.NFSProcedureRename, MapErrorToNFSStatus(err1))
		}
		return encodeNFSError(call.XID, nfs.NFSProcedureRename, MapErrorToNFSStatus(err2))
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
	err = exp.core.Rename(fromQPath, toQPath)
	postFrom, _ := statRecord(s, fromDirRec)
	postTo, _ := statRecord(s, toDirRec)
	if err != nil {
		return encodeRenameError(call.XID, MapErrorToNFSStatus(err), &preFrom, &postFrom, &preTo, &postTo, exp)
	}
	s.handles.UpdateOnRename(exp.core, fromDirRec.viewID, fromQPath, toQPath)
	if isClientPlaceholderName(toName) {
		exp.core.MarkTransientName(toQPath)
		s.stability.sidecars.notePlaceholder(exp.core, sidecarPathOf(fromQPath), toQPath)
		s.stability.absorbAtName(exp.core, s.handles, toDirRec.viewID, fromQPath)
	}
	s.stability.absorbAtName(exp.core, s.handles, toDirRec.viewID, toQPath)
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

// isClientPlaceholderName matches the name the macOS NFS client gives an open file it was asked to
// delete (".nfs.%08x.%04x" in xnu's nfs_sillyrename). The client removes it at last close. It is a
// protocol artifact of this adapter's client, so it must not reach a checkpoint.
func isClientPlaceholderName(name string) bool {
	const prefix = ".nfs."
	if len(name) != len(prefix)+8+1+4 || !strings.HasPrefix(name, prefix) || name[len(prefix)+8] != '.' {
		return false
	}
	for i := len(prefix); i < len(name); i++ {
		c := name[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !isHex && i != len(prefix)+8 {
			return false
		}
	}
	return true
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
	dirFH, name, err := readDirOpArg(call.Body)
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
	preTarget, err := statRecord(s, targetRec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureLink, MapErrorToNFSStatus(err))
	}
	preDir, err := statRecord(s, dirRec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureLink, MapErrorToNFSStatus(err))
	}
	if exp.cap.ReadOnly {
		return encodeLinkError(call.XID, nfs.NFSStatusROFS, &preTarget, &preDir, &preDir, exp)
	}
	if targetRec.qpath.RootID != dirRec.qpath.RootID {
		return encodeLinkError(call.XID, nfs.NFSStatusXDev, &preTarget, &preDir, &preDir, exp)
	}
	if !preDir.Mode.IsDir() {
		return encodeLinkError(call.XID, nfs.NFSStatusNotDir, &preTarget, &preDir, &preDir, exp)
	}
	linkQPath, err := joinChildPath(dirRec.qpath, name)
	if err != nil {
		return encodeLinkError(call.XID, MapErrorToNFSStatus(err), &preTarget, &preDir, &preDir, exp)
	}
	postTarget, err := exp.core.Link(targetRec.qpath, linkQPath)
	postDir, _ := statRecord(s, dirRec)
	if err != nil {
		return encodeLinkError(call.XID, MapErrorToNFSStatus(err), &preTarget, &preDir, &postDir, exp)
	}
	_ = s.handles.Ensure(dirRec.viewID, linkQPath, postTarget, dirRec.key.nodeID, false)
	postTarget = wireMeta(s, dirRec.key.viewHash, postTarget)
	s.dirs.InvalidateDir(dirRec.key)
	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writePostOpAttr(&body, &postTarget, exp.cap.AllowedUID, exp.cap.AllowedGID)
	writeWccData(&body, &preDir, &postDir, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func encodeLinkError(
	xid uint32, st nfs.NFSStatus, target, preDir, postDir *layeredfs.EntryAttr, exp *exportEntry,
) []byte {
	var body bytes.Buffer
	writeUint32(&body, uint32(st))
	writePostOpAttr(&body, target, exp.cap.AllowedUID, exp.cap.AllowedGID)
	writeWccData(&body, preDir, postDir, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(xid, nfs.ResponseCodeSuccess, body.Bytes())
}

func rebindRecordPathLocked(t *handleTable, rec *handleRecord, oldPK pathKey, newQP layeredfs.QualifiedPath) {
	delete(t.byPath, oldPK)
	delete(rec.paths, oldPK)
	if rec.paths == nil {
		rec.paths = make(map[pathKey]layeredfs.QualifiedPath)
	}
	newPK := makePathKey(rec.viewID, newQP)
	rec.paths[newPK], rec.qpath = newQP, newQP
	t.byPath[newPK] = rec
}

func (t *handleTable) UpdateOnRename(core *layeredfs.Core, viewID ViewID, from, to layeredfs.QualifiedPath) {
	t.mu.Lock()
	from.Rel, to.Rel = cleanRel(from.Rel), cleanRel(to.Rel)
	fromPK, toPK := makePathKey(viewID, from), makePathKey(viewID, to)
	srcRec := t.byPath[fromPK]
	var toClose layeredfs.HandleID
	if dstRec, ok := t.byPath[toPK]; ok && dstRec != srcRec {
		toClose = t.dropRecordPathLocked(dstRec, toPK, to)
	}
	prefix := from.Rel + "/"
	for pk, rec := range t.byPath {
		if pk.viewID != viewID || pk.root != from.RootID {
			continue
		}
		if pk.relPath == from.Rel {
			rebindRecordPathLocked(t, rec, pk, to)
		} else if from.Rel != "." && strings.HasPrefix(pk.relPath, prefix) {
			newRel := strings.TrimPrefix(pk.relPath, prefix)
			if to.Rel != "." {
				newRel = to.Rel + "/" + newRel
			}
			rebindRecordPathLocked(t, rec, pk, layeredfs.QualifiedPath{RootID: to.RootID, Rel: newRel})
		}
	}
	t.mu.Unlock()
	if core != nil && toClose != 0 {
		_ = core.CloseHandle(toClose)
	}
}
