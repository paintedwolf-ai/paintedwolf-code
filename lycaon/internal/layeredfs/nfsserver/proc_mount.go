package nfsserver

import (
	"bytes"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
	nfs "github.com/willscott/go-nfs"
)

func dispatchMount(s *Server, cs *connState, call rpcCallHeader) []byte {
	switch nfs.MountProcedure(call.Proc) {
	case nfs.MountProcNull:
		return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, nil)
	case nfs.MountProcMount:
		return onMount(s, cs, call)
	case nfs.MountProcUmnt:
		return onUmnt(s, cs, call)
	case nfs.MountProcUmntAll:
		return onUmntAll(s, cs, call)
	case nfs.MountProcDump, nfs.MountProcExport:
		var out bytes.Buffer
		writeUint32(&out, 0)
		return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, out.Bytes())
	default:
		return encodeAcceptedReply(call.XID, nfs.ResponseCodeProcUnavailable, nil)
	}
}

func onMount(s *Server, cs *connState, call rpcCallHeader) []byte {
	dirPath, err := readStringBounded(call.Body, nfs.MntPathLen)
	if err != nil {
		return encodeMountStatus(call.XID, nfs.MountStatusErrInval)
	}
	dirPath = "/" + strings.Trim(dirPath, "/")
	if call.Cred.Flavor != nfs.AuthFlavorUnix {
		return encodeMountStatus(call.XID, nfs.MountStatusErrAcces)
	}

	s.state.mu.Lock()
	entry, ok := s.state.exportsByID[dirPath]
	if !ok || entry.cap.AllowedUID != call.Cred.UID {
		s.state.mu.Unlock()
		return encodeMountStatus(call.XID, nfs.MountStatusErrAcces)
	}
	if !cs.bindView(entry.viewHash) {
		s.state.mu.Unlock()
		return encodeMountStatus(call.XID, nfs.MountStatusErrAcces)
	}
	entry.mounted = true
	entry.activeConn = cs
	capCfg := entry.cap
	viewCore := entry.core
	s.state.mu.Unlock()

	rootQPath := layeredfs.QualifiedPath{RootID: capCfg.Root, Rel: "."}
	meta, err := viewCore.Lstat(rootQPath)
	if err != nil {
		return encodeMountStatus(call.XID, nfs.MountStatusErrNoEnt)
	}
	fh := s.handles.Ensure(capCfg.ViewID, rootQPath, meta, true, false)

	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.MountStatusOk))
	writeOpaque(&body, fh[:])
	writeUint32(&body, 1)
	writeUint32(&body, uint32(nfs.AuthFlavorUnix))
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func onUmnt(s *Server, cs *connState, call rpcCallHeader) []byte {
	dirPath, err := readStringBounded(call.Body, nfs.MntPathLen)
	if err != nil {
		return encodeAcceptedReply(call.XID, nfs.ResponseCodeGarbageArgs, nil)
	}
	dirPath = "/" + strings.Trim(dirPath, "/")
	s.state.mu.Lock()
	if entry, ok := s.state.exportsByID[dirPath]; ok && call.Cred.Flavor == nfs.AuthFlavorUnix && entry.cap.AllowedUID == call.Cred.UID {
		entry.mounted = false
		if entry.activeConn == cs {
			entry.activeConn = nil
		}
		cs.clearView(entry.viewHash)
	}
	s.state.mu.Unlock()
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, nil)
}

func onUmntAll(s *Server, cs *connState, call rpcCallHeader) []byte {
	vHash := cs.getBoundView()
	if vHash != 0 && call.Cred.Flavor == nfs.AuthFlavorUnix {
		s.state.mu.Lock()
		for _, entry := range s.state.exportsByVH[vHash] {
			if entry.cap.AllowedUID == call.Cred.UID {
				entry.mounted = false
				if entry.activeConn == cs {
					entry.activeConn = nil
				}
			}
		}
		s.state.mu.Unlock()
		cs.clearView(vHash)
	}
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, nil)
}

func encodeMountStatus(xid uint32, st nfs.MountStatus) []byte {
	var body bytes.Buffer
	writeUint32(&body, uint32(st))
	return encodeAcceptedReply(xid, nfs.ResponseCodeSuccess, body.Bytes())
}

func dispatchNFS(s *Server, cs *connState, call rpcCallHeader) []byte {
	proc := nfs.NFSProcedure(call.Proc)
	switch proc {
	case nfs.NFSProcedureNull:
		return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, nil)
	case nfs.NFSProcedureGetAttr:
		return onGetAttr(s, cs, call)
	case nfs.NFSProcedureSetAttr:
		return onSetAttr(s, cs, call)
	case nfs.NFSProcedureLookup:
		return onLookup(s, cs, call)
	case nfs.NFSProcedureAccess:
		return onAccess(s, cs, call)
	case nfs.NFSProcedureReadlink:
		return onReadlink(s, cs, call)
	case nfs.NFSProcedureRead:
		return onRead(s, cs, call)
	case nfs.NFSProcedureWrite:
		return onWrite(s, cs, call)
	case nfs.NFSProcedureCreate:
		return onCreate(s, cs, call)
	case nfs.NFSProcedureMkDir:
		return onMkdir(s, cs, call)
	case nfs.NFSProcedureSymlink:
		return onSymlink(s, cs, call)
	case nfs.NFSProcedureMkNod:
		return onMknod(s, cs, call)
	case nfs.NFSProcedureRemove:
		return onRemove(s, cs, call)
	case nfs.NFSProcedureRmDir:
		return onRmdir(s, cs, call)
	case nfs.NFSProcedureRename:
		return onRename(s, cs, call)
	case nfs.NFSProcedureLink:
		return onLink(s, cs, call)
	case nfs.NFSProcedureReadDir:
		return onReaddir(s, cs, call)
	case nfs.NFSProcedureReadDirPlus:
		return onReaddirPlus(s, cs, call)
	case nfs.NFSProcedureFSStat:
		return onFSStat(s, cs, call)
	case nfs.NFSProcedureFSInfo:
		return onFSInfo(s, cs, call)
	case nfs.NFSProcedurePathConf:
		return onPathConf(s, cs, call)
	case nfs.NFSProcedureCommit:
		return onCommit(s, cs, call)
	default:
		return encodeAcceptedReply(call.XID, nfs.ResponseCodeProcUnavailable, nil)
	}
}

func encodeNFSError(xid uint32, proc nfs.NFSProcedure, status nfs.NFSStatus) []byte {
	var body bytes.Buffer
	writeUint32(&body, uint32(status))
	writeErrorFormattingTail(&body, proc)
	return encodeAcceptedReply(xid, nfs.ResponseCodeSuccess, body.Bytes())
}

// writeErrorFormattingTail appends RFC 1813 fail-case post_op_attr / wcc_data zero fields.
func writeErrorFormattingTail(w *bytes.Buffer, proc nfs.NFSProcedure) {
	switch proc {
	case nfs.NFSProcedureGetAttr:
	case nfs.NFSProcedureLookup,
		nfs.NFSProcedureAccess,
		nfs.NFSProcedureReadlink,
		nfs.NFSProcedureRead,
		nfs.NFSProcedureReadDir,
		nfs.NFSProcedureReadDirPlus,
		nfs.NFSProcedureFSStat,
		nfs.NFSProcedureFSInfo,
		nfs.NFSProcedurePathConf:
		writeUint32(w, 0)
	case nfs.NFSProcedureSetAttr,
		nfs.NFSProcedureWrite,
		nfs.NFSProcedureCreate,
		nfs.NFSProcedureMkDir,
		nfs.NFSProcedureSymlink,
		nfs.NFSProcedureMkNod,
		nfs.NFSProcedureRemove,
		nfs.NFSProcedureRmDir,
		nfs.NFSProcedureCommit:
		writeUint64(w, 0)
	case nfs.NFSProcedureLink:
		writeUint32(w, 0)
		writeUint64(w, 0)
	case nfs.NFSProcedureRename:
		writeUint64(w, 0)
		writeUint64(w, 0)
	}
}

func encodePostOpError(xid uint32, st nfs.NFSStatus, meta *layeredfs.EntryAttr, exp *exportEntry) []byte {
	var body bytes.Buffer
	writeUint32(&body, uint32(st))
	writePostOpAttr(&body, meta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(xid, nfs.ResponseCodeSuccess, body.Bytes())
}

func encodeWccError(xid uint32, st nfs.NFSStatus, pre, post *layeredfs.EntryAttr, exp *exportEntry) []byte {
	var body bytes.Buffer
	writeUint32(&body, uint32(st))
	writeWccData(&body, pre, post, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(xid, nfs.ResponseCodeSuccess, body.Bytes())
}

func joinChildPath(parent layeredfs.QualifiedPath, name string) (layeredfs.QualifiedPath, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") || strings.ContainsRune(name, 0) {
		return layeredfs.QualifiedPath{}, syscall.EINVAL
	}
	if len(name) > nfs.MNTNameLen {
		return layeredfs.QualifiedPath{}, syscall.ENAMETOOLONG
	}
	rel := name
	if cleanRel(parent.Rel) != "." {
		rel = parent.Rel + "/" + name
	}
	return layeredfs.QualifiedPath{RootID: parent.RootID, Rel: rel}, nil
}
