package nfsserver

import (
	"bytes"
	"os"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	nfs "github.com/willscott/go-nfs"
)

const (
	createUnchecked = 0
	createGuarded   = 1
	createExclusive = 2
)

func onCreate(s *Server, cs *connState, call rpcCallHeader) []byte {
	dirFH, name, err := readDirOpArg(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureCreate, nfs.NFSStatusInval)
	}
	how, err := readUint32(call.Body)
	if err != nil || how > createExclusive {
		return encodeNFSError(call.XID, nfs.NFSProcedureCreate, nfs.NFSStatusInval)
	}
	var (
		attrs      sattr3
		createVerf [8]byte
	)
	if how == createExclusive {
		verfBytes, err := readFixedBytes(call.Body, 8)
		if err != nil {
			return encodeNFSError(call.XID, nfs.NFSProcedureCreate, nfs.NFSStatusInval)
		}
		copy(createVerf[:], verfBytes)
	} else {
		if attrs, err = readSattr3(call.Body, time.Now()); err != nil {
			return encodeNFSError(call.XID, nfs.NFSProcedureCreate, nfs.NFSStatusInval)
		}
	}

	dirRec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, dirFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureCreate, MapErrorToNFSStatus(err))
	}
	preDir, err := statRecord(s, dirRec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureCreate, MapErrorToNFSStatus(err))
	}
	if exp.cap.ReadOnly {
		return encodeWccError(call.XID, nfs.NFSStatusROFS, &preDir, &preDir, exp)
	}
	if !preDir.Mode.IsDir() {
		return encodeWccError(call.XID, nfs.NFSStatusNotDir, &preDir, &preDir, exp)
	}
	childQPath, err := joinChildPath(dirRec.qpath, name)
	if err != nil {
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preDir, &preDir, exp)
	}

	fh, fileMeta, err := executeCreate(s, exp.core, dirRec, childQPath, how, attrs, createVerf)
	postDir, _ := statRecord(s, dirRec)
	if err != nil {
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preDir, &postDir, exp)
	}
	s.dirs.InvalidateDir(dirRec.key)

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writePostOpFH3(&body, fh[:])
	writePostOpAttr(&body, &fileMeta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	writeWccData(&body, &preDir, &postDir, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func executeCreate(
	s *Server, viewCore *layeredfs.Core, dirRec *handleRecord, childQPath layeredfs.QualifiedPath,
	how uint32, attrs sattr3, createVerf [8]byte,
) ([HandleSize]byte, layeredfs.EntryAttr, error) {
	var zeroFH [HandleSize]byte
	existingMeta, statErr := viewCore.Lstat(childQPath)
	if statErr == nil {
		if existingMeta.Mode.IsDir() {
			return zeroFH, layeredfs.EntryAttr{}, syscall.EISDIR
		}
		if how == createGuarded {
			return zeroFH, layeredfs.EntryAttr{}, os.ErrExist
		}
		if how == createExclusive {
			if rec, ok := s.handles.LookupByPath(dirRec.viewID, childQPath); ok && rec.createVerf != nil && *rec.createVerf == createVerf {
				return rec.wire, existingMeta, nil
			}
			return zeroFH, layeredfs.EntryAttr{}, os.ErrExist
		}
	}

	perm := os.FileMode(0o644)
	ownerRO := false
	if how != createExclusive && attrs.HasMode {
		perm = attrs.Mode & os.ModePerm
		if perm&0o200 == 0 {
			ownerRO = true
		}
	}
	openFlags := layeredfs.OpenReadWrite | layeredfs.OpenCreate
	if how == createGuarded || how == createExclusive {
		openFlags |= layeredfs.OpenExclusive
	} else if !attrs.HasSize || attrs.Size == 0 {
		openFlags |= layeredfs.OpenTruncate
	}
	createPerm := perm
	if ownerRO {
		createPerm = perm | 0o200
	}
	fd, _, err := viewCore.Open(childQPath, openFlags, createPerm)
	if err != nil {
		return zeroFH, layeredfs.EntryAttr{}, err
	}
	_ = viewCore.CloseHandle(fd)

	if how != createExclusive {
		if attrs.HasMode {
			_, _ = viewCore.Chmod(childQPath, perm)
		}
		if attrs.HasAtime || attrs.HasMtime {
			now := time.Now()
			at, mt := now, now
			if attrs.HasAtime {
				at = attrs.Atime
			}
			if attrs.HasMtime {
				mt = attrs.Mtime
			}
			_, _ = viewCore.Chtimes(childQPath, at, mt, true)
		}
	}
	meta, err := viewCore.Lstat(childQPath)
	if err != nil {
		return zeroFH, layeredfs.EntryAttr{}, err
	}
	fh := s.handles.Ensure(dirRec.viewID, childQPath, meta, false, ownerRO)
	if how == createExclusive {
		if rec, ok := s.handles.LookupByPath(dirRec.viewID, childQPath); ok {
			s.handles.SetCreateVerifier(rec.key, createVerf)
		}
	}
	return fh, meta, nil
}

func onMkdir(s *Server, cs *connState, call rpcCallHeader) []byte {
	dirFH, name, err := readDirOpArg(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureMkDir, nfs.NFSStatusInval)
	}
	attrs, err := readSattr3(call.Body, time.Now())
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureMkDir, nfs.NFSStatusInval)
	}
	dirRec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, dirFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureMkDir, MapErrorToNFSStatus(err))
	}
	preDir, err := statRecord(s, dirRec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureMkDir, MapErrorToNFSStatus(err))
	}
	if exp.cap.ReadOnly {
		return encodeWccError(call.XID, nfs.NFSStatusROFS, &preDir, &preDir, exp)
	}
	childQPath, err := joinChildPath(dirRec.qpath, name)
	if err != nil {
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preDir, &preDir, exp)
	}
	perm := os.FileMode(0o755)
	if attrs.HasMode {
		perm = attrs.Mode & os.ModePerm
	}
	if _, err := exp.core.Mkdir(childQPath, perm); err != nil {
		postDir, _ := statRecord(s, dirRec)
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preDir, &postDir, exp)
	}
	if attrs.HasAtime || attrs.HasMtime {
		now := time.Now()
		at, mt := now, now
		if attrs.HasAtime {
			at = attrs.Atime
		}
		if attrs.HasMtime {
			mt = attrs.Mtime
		}
		_, _ = exp.core.Chtimes(childQPath, at, mt, true)
	}
	s.dirs.InvalidateDir(dirRec.key)
	childMeta, err := exp.core.Lstat(childQPath)
	postDir, _ := statRecord(s, dirRec)
	if err != nil {
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preDir, &postDir, exp)
	}
	fh := s.handles.Ensure(dirRec.viewID, childQPath, childMeta, false, false)

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writePostOpFH3(&body, fh[:])
	writePostOpAttr(&body, &childMeta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	writeWccData(&body, &preDir, &postDir, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func onSymlink(s *Server, cs *connState, call rpcCallHeader) []byte {
	dirFH, name, err := readDirOpArg(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureSymlink, nfs.NFSStatusInval)
	}
	if _, err := readSattr3(call.Body, time.Now()); err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureSymlink, nfs.NFSStatusInval)
	}
	target, err := readStringBounded(call.Body, nfs.MntPathLen)
	if err != nil || target == "" {
		return encodeNFSError(call.XID, nfs.NFSProcedureSymlink, nfs.NFSStatusInval)
	}
	dirRec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, dirFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureSymlink, MapErrorToNFSStatus(err))
	}
	preDir, err := statRecord(s, dirRec)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureSymlink, MapErrorToNFSStatus(err))
	}
	if exp.cap.ReadOnly {
		return encodeWccError(call.XID, nfs.NFSStatusROFS, &preDir, &preDir, exp)
	}
	childQPath, err := joinChildPath(dirRec.qpath, name)
	if err != nil {
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preDir, &preDir, exp)
	}
	if _, err := exp.core.Symlink(target, childQPath); err != nil {
		postDir, _ := statRecord(s, dirRec)
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preDir, &postDir, exp)
	}
	s.dirs.InvalidateDir(dirRec.key)
	childMeta, err := exp.core.Lstat(childQPath)
	postDir, _ := statRecord(s, dirRec)
	if err != nil {
		return encodeWccError(call.XID, MapErrorToNFSStatus(err), &preDir, &postDir, exp)
	}
	fh := s.handles.Ensure(dirRec.viewID, childQPath, childMeta, false, false)

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.NFSStatusOk))
	writePostOpFH3(&body, fh[:])
	writePostOpAttr(&body, &childMeta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	writeWccData(&body, &preDir, &postDir, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func onMknod(s *Server, cs *connState, call rpcCallHeader) []byte {
	dirFH, _, err := readDirOpArg(call.Body)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureMkNod, nfs.NFSStatusInval)
	}
	dirRec, exp, err := resolveAndAuthHandle(s, cs, call.Cred, dirFH)
	if err != nil {
		return encodeNFSError(call.XID, nfs.NFSProcedureMkNod, MapErrorToNFSStatus(err))
	}
	dirMeta, _ := statRecord(s, dirRec)
	return encodeWccError(call.XID, nfs.NFSStatusNotSupp, &dirMeta, &dirMeta, exp)
}
