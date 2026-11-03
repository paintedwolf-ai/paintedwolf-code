package nfsserver

import (
	"bytes"
	"context"
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

type serverBarrierGate struct {
	mu       sync.Mutex
	cond     *sync.Cond
	inFlight int
	gatedVH  map[uint64]bool
}

var mountHandshakes, serverGates sync.Map

func getServerGate(s *Server) *serverBarrierGate {
	if v, ok := serverGates.Load(s); ok { return v.(*serverBarrierGate) }
	g := &serverBarrierGate{gatedVH: make(map[uint64]bool)}
	g.cond = sync.NewCond(&g.mu)
	actual, _ := serverGates.LoadOrStore(s, g)
	return actual.(*serverBarrierGate)
}

// BeginBarrierDrain gates new mutating NFSv3 RPCs for viewID on s, waits for in-flight mutating
// RPCs to finish, and flushes all staged UNSTABLE writes to Core before Core's barrier freezes mutations.
func (s *Server) BeginBarrierDrain(ctx context.Context, viewID ViewID) (func(), error) {
	if err := ctx.Err(); err != nil { return nil, err }
	vHash, g := computeViewHash(viewID), getServerGate(s)
	g.mu.Lock()
	if g.gatedVH[vHash] { g.mu.Unlock(); return nil, layeredfs.ErrBarrierActive }
	g.gatedVH[vHash] = true
	for g.inFlight > 0 { g.cond.Wait() }
	g.mu.Unlock()
	release := func() { g.mu.Lock(); delete(g.gatedVH, vHash); g.mu.Unlock() }
	if err := s.FlushForCheckpoint(ctx, viewID); err != nil { release(); return nil, err }
	var once sync.Once
	return func() { once.Do(release) }, nil
}

// LastMountHandshake returns the most recent MOUNTPROC3_MNT/UMNT credential observation for exportPath on s.
func (s *Server) LastMountHandshake(exportPath string) (MountHandshakeObservation, bool) {
	if s != nil {
		if v, ok := mountHandshakes.Load("/" + strings.Trim(exportPath, "/")); ok { return v.(MountHandshakeObservation), true }
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
	return cred.Flavor == nfs.AuthFlavorUnix && ((cred.UID == capCfg.AllowedUID && cred.GID == capCfg.AllowedGID) || (cred.UID == 0 && cred.GID == 0))
}

func recordHandshake(dirPath string, capCfg ExportCapability, cred authCred, mounted bool) {
	mountHandshakes.Store(dirPath, MountHandshakeObservation{
		ExportPath: dirPath, ViewID: capCfg.ViewID, Root: capCfg.Root,
		ObservedUID: cred.UID, ObservedGID: cred.GID, AllowedUID: capCfg.AllowedUID, AllowedGID: capCfg.AllowedGID, Mounted: mounted,
	})
}

func onMount(s *Server, cs *connState, call rpcCallHeader) []byte {
	dirPath, err := readStringBounded(call.Body, nfs.MntPathLen)
	if err != nil { return encodeMountStatus(call.XID, nfs.MountStatusErrInval) }
	dirPath = "/" + strings.Trim(dirPath, "/")
	s.state.mu.Lock()
	entry, ok := s.state.exportsByID[dirPath]
	if !ok || !allowMountCred(entry.cap, call.Cred) || !cs.bindView(entry.viewHash) {
		cs.denyMount(); s.state.mu.Unlock()
		return encodeMountStatus(call.XID, nfs.MountStatusErrAcces)
	}
	entry.mounted = true
	capCfg, viewCore := entry.cap, entry.core
	s.state.mu.Unlock()
	recordHandshake(dirPath, capCfg, call.Cred, true)
	rootQPath := layeredfs.QualifiedPath{RootID: capCfg.Root, Rel: "."}
	meta, err := viewCore.Lstat(rootQPath)
	if err != nil { return encodeMountStatus(call.XID, nfs.MountStatusErrNoEnt) }
	fh := s.handles.Ensure(capCfg.ViewID, rootQPath, meta, true, false)
	var body bytes.Buffer
	writeUint32(&body, uint32(nfs.MountStatusOk)); writeOpaque(&body, fh[:])
	writeUint32(&body, 1); writeUint32(&body, uint32(nfs.AuthFlavorUnix))
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, body.Bytes())
}

func onUmnt(s *Server, cs *connState, call rpcCallHeader) []byte {
	dirPath, err := readStringBounded(call.Body, nfs.MntPathLen)
	if err != nil { return encodeAcceptedReply(call.XID, nfs.ResponseCodeGarbageArgs, nil) }
	dirPath = "/" + strings.Trim(dirPath, "/")
	s.state.mu.Lock()
	if entry, ok := s.state.exportsByID[dirPath]; ok && allowMountCred(entry.cap, call.Cred) {
		entry.mounted = false; cs.clearView(entry.viewHash); recordHandshake(dirPath, entry.cap, call.Cred, false)
	}
	s.state.mu.Unlock()
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, nil)
}

func onUmntAll(s *Server, cs *connState, call rpcCallHeader) []byte {
	if call.Cred.Flavor == nfs.AuthFlavorUnix {
		vHash := cs.getBoundView()
		s.state.mu.Lock()
		for vh, entries := range s.state.exportsByVH {
			if vHash == 0 || vh == vHash {
				for _, entry := range entries {
					if allowMountCred(entry.cap, call.Cred) {
						entry.mounted = false
					}
				}
			}
		}
		s.state.mu.Unlock(); cs.clearView(vHash)
	}
	return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, nil)
}

func encodeMountStatus(xid uint32, st nfs.MountStatus) []byte {
	var body bytes.Buffer
	writeUint32(&body, uint32(st))
	return encodeAcceptedReply(xid, nfs.ResponseCodeSuccess, body.Bytes())
}

var nfsHandlers = map[nfs.NFSProcedure]func(*Server, *connState, rpcCallHeader) []byte{
	nfs.NFSProcedureGetAttr: onGetAttr, nfs.NFSProcedureSetAttr: onSetAttr, nfs.NFSProcedureLookup: onLookup,
	nfs.NFSProcedureAccess: onAccess, nfs.NFSProcedureReadlink: onReadlink, nfs.NFSProcedureRead: onRead,
	nfs.NFSProcedureWrite: onWrite, nfs.NFSProcedureCreate: onCreate, nfs.NFSProcedureMkDir: onMkdir,
	nfs.NFSProcedureSymlink: onSymlink, nfs.NFSProcedureMkNod: onMknod, nfs.NFSProcedureRemove: onRemove,
	nfs.NFSProcedureRmDir: onRmdir, nfs.NFSProcedureRename: onRename, nfs.NFSProcedureLink: onLink,
	nfs.NFSProcedureReadDir: onReaddir, nfs.NFSProcedureReadDirPlus: onReaddirPlus,
	nfs.NFSProcedureFSStat: onFSStat, nfs.NFSProcedureFSInfo: onFSInfo, nfs.NFSProcedurePathConf: onPathConf,
	nfs.NFSProcedureCommit: onCommit,
}

func isMutatingNFSProc(proc nfs.NFSProcedure) bool {
	switch proc {
	case nfs.NFSProcedureSetAttr, nfs.NFSProcedureWrite, nfs.NFSProcedureCreate, nfs.NFSProcedureMkDir, nfs.NFSProcedureSymlink,
		nfs.NFSProcedureMkNod, nfs.NFSProcedureRemove, nfs.NFSProcedureRmDir, nfs.NFSProcedureRename, nfs.NFSProcedureLink:
		return true
	}
	return false
}

func peekCallViewHash(s *Server, cs *connState, call rpcCallHeader) uint64 {
	if vh := cs.getBoundView(); vh != 0 { return vh }
	if call.Body != nil {
		dup := *call.Body
		if rawFH, err := readFileHandle(&dup); err == nil {
			if rec, rErr := s.handles.Resolve(rawFH, 0); rErr == nil { return rec.key.viewHash }
		}
	}
	return 0
}

func dispatchNFS(s *Server, cs *connState, call rpcCallHeader) []byte {
	proc := nfs.NFSProcedure(call.Proc)
	if proc == nfs.NFSProcedureNull { return encodeAcceptedReply(call.XID, nfs.ResponseCodeSuccess, nil) }
	fn, ok := nfsHandlers[proc]
	if !ok { return encodeAcceptedReply(call.XID, nfs.ResponseCodeProcUnavailable, nil) }
	if isMutatingNFSProc(proc) {
		vh, g := peekCallViewHash(s, cs, call), getServerGate(s)
		g.mu.Lock()
		if g.gatedVH[vh] { g.mu.Unlock(); return encodeNFSError(call.XID, proc, nfs.NFSStatusJukebox) }
		g.inFlight++; g.mu.Unlock()
		defer func() {
			g.mu.Lock()
			if g.inFlight--; g.inFlight == 0 { g.cond.Broadcast() }
			g.mu.Unlock()
		}()
	}
	return fn(s, cs, call)
}

func encodeNFSError(xid uint32, proc nfs.NFSProcedure, status nfs.NFSStatus) []byte {
	var body bytes.Buffer
	writeUint32(&body, uint32(status)); writeErrorFormattingTail(&body, proc)
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
		writeUint32(w, 0); writeUint64(w, 0)
	case nfs.NFSProcedureRename:
		writeUint64(w, 0); writeUint64(w, 0)
	}
}

func encodePostOpError(xid uint32, st nfs.NFSStatus, meta *layeredfs.EntryAttr, exp *exportEntry) []byte {
	var body bytes.Buffer
	writeUint32(&body, uint32(st)); writePostOpAttr(&body, meta, exp.cap.AllowedUID, exp.cap.AllowedGID)
	return encodeAcceptedReply(xid, nfs.ResponseCodeSuccess, body.Bytes())
}

func encodeWccError(xid uint32, st nfs.NFSStatus, pre, post *layeredfs.EntryAttr, exp *exportEntry) []byte {
	var body bytes.Buffer
	writeUint32(&body, uint32(st)); writeWccData(&body, pre, post, exp.cap.AllowedUID, exp.cap.AllowedGID)
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
	newStart, newEnd, next, inserted := added.offset, added.offset+uint64(len(added.data)), make([]unstableChunk, 0, len(chunks)+2), false
	for _, ch := range chunks {
		chStart, chEnd := ch.offset, ch.offset+uint64(len(ch.data))
		if chEnd <= newStart {
			next = append(next, ch); continue
		}
		if chStart < newEnd && chStart < newStart {
			next = append(next, unstableChunk{seq: ch.seq, offset: chStart, data: append([]byte(nil), ch.data[:newStart-chStart]...)})
		}
		if !inserted {
			next, inserted = append(next, added), true
		}
		if chStart >= newEnd {
			next = append(next, ch)
		} else if chEnd > newEnd {
			next = append(next, unstableChunk{seq: ch.seq, offset: newEnd, data: append([]byte(nil), ch.data[newEnd-chStart:]...)})
		}
	}
	if !inserted {
		next = append(next, added)
	}
	return coalesceAdjacentChunks(next, flushingSeq)
}

func coalesceAdjacentChunks(chunks []unstableChunk, flushingSeq uint64) []unstableChunk {
	out := make([]unstableChunk, 0, len(chunks))
	for _, cur := range chunks {
		if n := len(out); n > 0 && out[n-1].offset+uint64(len(out[n-1].data)) == cur.offset && out[n-1].seq > flushingSeq && cur.seq > flushingSeq {
			out[n-1].data, out[n-1].seq = append(append([]byte(nil), out[n-1].data...), cur.data...), max(out[n-1].seq, cur.seq)
		} else {
			out = append(out, cur)
		}
	}
	return out
}
