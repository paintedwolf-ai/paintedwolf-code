package nfsserver

import (
	"bytes"
	"strings"
	"sync"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
	nfs "github.com/willscott/go-nfs"
)

// MountHandshakeObservation records the observed RPC AUTH_UNIX credentials from MOUNTPROC3_MNT
// separately from the worker data-operation authorization (AllowedUID, AllowedGID) enforced on NFSPROC3_* RPCs.
type MountHandshakeObservation struct {
	ExportPath, ViewID, Root            string
	ObservedUID, ObservedGID            uint32
	AllowedUID, AllowedGID              uint32
	Mounted                             bool
}

var mountHandshakes sync.Map

// LastMountHandshake returns the most recent MOUNTPROC3_MNT/UMNT credential observation for exportPath on s.
func (s *Server) LastMountHandshake(exportPath string) (MountHandshakeObservation, bool) {
	if s == nil {
		return MountHandshakeObservation{}, false
	}
	if v, ok := mountHandshakes.Load("/" + strings.Trim(exportPath, "/")); ok {
		obs, ok := v.(MountHandshakeObservation)
		return obs, ok
	}
	return MountHandshakeObservation{}, false
}

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

func allowMountCred(capCfg ExportCapability, cred authCred) bool {
	return cred.Flavor == nfs.AuthFlavorUnix &&
		((cred.UID == capCfg.AllowedUID && cred.GID == capCfg.AllowedGID) || (cred.UID == 0 && cred.GID == 0))
}

func recordHandshake(dirPath string, capCfg ExportCapability, cred authCred, mounted bool) {
	mountHandshakes.Store(dirPath, MountHandshakeObservation{
		ExportPath: dirPath, ViewID: capCfg.ViewID, Root: capCfg.Root,
		ObservedUID: cred.UID, ObservedGID: cred.GID,
		AllowedUID: capCfg.AllowedUID, AllowedGID: capCfg.AllowedGID, Mounted: mounted,
	})
}

func onMount(s *Server, cs *connState, call rpcCallHeader) []byte {
	dirPath, err := readStringBounded(call.Body, nfs.MntPathLen)
	if err != nil {
		return encodeMountStatus(call.XID, nfs.MountStatusErrInval)
	}
	dirPath = "/" + strings.Trim(dirPath, "/")
	s.state.mu.Lock()
	entry, ok := s.state.exportsByID[dirPath]
	if !ok || !allowMountCred(entry.cap, call.Cred) || !cs.bindView(entry.viewHash) {
		cs.denyMount()
		s.state.mu.Unlock()
		return encodeMountStatus(call.XID, nfs.MountStatusErrAcces)
	}
	entry.mounted = true
	capCfg, viewCore := entry.cap, entry.core
	s.state.mu.Unlock()
	recordHandshake(dirPath, capCfg, call.Cred, true)

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
	if entry, ok := s.state.exportsByID[dirPath]; ok && allowMountCred(entry.cap, call.Cred) {
		entry.mounted = false
		cs.clearView(entry.viewHash)
		recordHandshake(dirPath, entry.cap, call.Cred, false)
	}
	s.state.mu.Unlock()
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, nil)
}

func onUmntAll(s *Server, cs *connState, call rpcCallHeader) []byte {
	if call.Cred.Flavor == nfs.AuthFlavorUnix {
		vHash := cs.getBoundView()
		s.state.mu.Lock()
		for vh, entries := range s.state.exportsByVH {
			if vHash != 0 && vh != vHash {
				continue
			}
			for _, entry := range entries {
				if allowMountCred(entry.cap, call.Cred) {
					entry.mounted = false
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
	switch nfs.NFSProcedure(call.Proc) {
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

func writeErrorFormattingTail(w *bytes.Buffer, proc nfs.NFSProcedure) {
	switch proc {
	case nfs.NFSProcedureLookup, nfs.NFSProcedureAccess, nfs.NFSProcedureReadlink, nfs.NFSProcedureRead,
		nfs.NFSProcedureReadDir, nfs.NFSProcedureReadDirPlus, nfs.NFSProcedureFSStat, nfs.NFSProcedureFSInfo, nfs.NFSProcedurePathConf:
		writeUint32(w, 0)
	case nfs.NFSProcedureSetAttr, nfs.NFSProcedureWrite, nfs.NFSProcedureCreate, nfs.NFSProcedureMkDir,
		nfs.NFSProcedureSymlink, nfs.NFSProcedureMkNod, nfs.NFSProcedureRemove, nfs.NFSProcedureRmDir, nfs.NFSProcedureCommit:
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
	if cleanRel(parent.Rel) != "." {
		name = parent.Rel + "/" + name
	}
	return layeredfs.QualifiedPath{RootID: parent.RootID, Rel: name}, nil
}

// PendingUnstableStats returns buffered unstable bytes and chunk count for viewID and total bytes on s.
func PendingUnstableStats(s *Server, viewID ViewID) (viewBytes, viewChunks, totalBytes int) {
	s.stability.mu.Lock()
	defer s.stability.mu.Unlock()
	return pendingStatsLocked(s.stability, computeViewHash(viewID))
}

func compactAndInsertChunk(chunks []unstableChunk, added unstableChunk, flushingSeq uint64) []unstableChunk {
	if len(added.data) == 0 {
		return chunks
	}
	newStart, newEnd := added.offset, added.offset+uint64(len(added.data))
	next, inserted := make([]unstableChunk, 0, len(chunks)+2), false
	for _, ch := range chunks {
		chStart, chEnd := ch.offset, ch.offset+uint64(len(ch.data))
		if chEnd <= newStart {
			next = append(next, ch)
			continue
		}
		if chStart >= newEnd {
			if !inserted {
				next, inserted = append(next, added), true
			}
			next = append(next, ch)
			continue
		}
		if chStart < newStart {
			next = append(next, unstableChunk{seq: ch.seq, offset: chStart, data: append([]byte(nil), ch.data[:newStart-chStart]...)})
		}
		if !inserted {
			next, inserted = append(next, added), true
		}
		if chEnd > newEnd {
			next = append(next, unstableChunk{seq: ch.seq, offset: newEnd, data: append([]byte(nil), ch.data[newEnd-chStart:]...)})
		}
	}
	if !inserted {
		next = append(next, added)
	}
	return coalesceAdjacentChunks(next, flushingSeq)
}

func coalesceAdjacentChunks(chunks []unstableChunk, flushingSeq uint64) []unstableChunk {
	if len(chunks) <= 1 {
		return chunks
	}
	out := append(make([]unstableChunk, 0, len(chunks)), chunks[0])
	for i := 1; i < len(chunks); i++ {
		if prev, cur := &out[len(out)-1], chunks[i]; prev.offset+uint64(len(prev.data)) == cur.offset && prev.seq > flushingSeq && cur.seq > flushingSeq {
			prev.data = append(append([]byte(nil), prev.data...), cur.data...)
			prev.seq = max(prev.seq, cur.seq)
		} else {
			out = append(out, cur)
		}
	}
	return out
}
