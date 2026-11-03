//go:build linux

package fuselinux

import (
	"encoding/binary"
	"errors"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"golang.org/x/sys/unix"
)

const (
	fuseCapPOSIXLocks   = 1 << 1
	fuseCapAtomicOTrunc = 1 << 3
	fuseCapBigWrites    = 1 << 5
	fuseCapFlockLocks   = 1 << 10

	fattrMode     = 1 << 0
	fattrUID      = 1 << 1
	fattrGID      = 1 << 2
	fattrSize     = 1 << 3
	fattrAtime    = 1 << 4
	fattrMtime    = 1 << 5
	fattrFH       = 1 << 6
	fattrAtimeNow = 1 << 7
	fattrMtimeNow = 1 << 8
)

func (s *Server) serveRequest(st *fuseRequestState, req []byte) {
	defer s.requests.unregister(st.unique)
	opcode := binary.LittleEndian.Uint32(req[4:8])
	nodeID := binary.LittleEndian.Uint64(req[16:24])
	body := req[fuseHeaderInSize:]

	var payload []byte
	var errno int32
	switch opcode {
	case fuseOpInit:
		payload, errno = s.opInit(body)
	case fuseOpLookup:
		payload, errno = s.opLookup(nodeID, body)
	case fuseOpGetattr:
		payload, errno = s.opGetattr(nodeID, body)
	case fuseOpReadlink:
		payload, errno = s.opReadlink(nodeID)
	case fuseOpOpen:
		payload, errno = s.opOpen(st, nodeID, body)
	case fuseOpRead:
		payload, errno = s.opRead(body)
	case fuseOpStatfs:
		payload, errno = s.opStatfs()
	case fuseOpRelease:
		payload, errno = s.opRelease(nodeID, body)
	case fuseOpFsync:
		payload, errno = s.opFsync(body)
	case fuseOpFsyncdir:
		payload, errno = s.opFsyncdir()
	case fuseOpFlush:
		payload, errno = s.opFlush(nodeID, body)
	case fuseOpOpendir:
		payload, errno = s.opOpendir(nodeID)
	case fuseOpReaddir:
		payload, errno = s.opReaddir(nodeID, body)
	case fuseOpReleasedir:
		payload, errno = s.opReleasedir(body)
	case fuseOpGetxattr:
		payload, errno = s.opGetxattr(nodeID, body)
	case fuseOpListxattr:
		payload, errno = s.opListxattr(nodeID, body)
	case fuseOpGetlk:
		payload, errno = s.opGetlk(nodeID, body)
	case fuseOpSetlk, fuseOpSetlkw:
		payload, errno = s.opSetlk(st, nodeID, body)
	case fuseOpDestroy:
		payload, errno = nil, 0
	case fuseOpPoll:
		s.pollRequests.Add(1)
		payload, errno = pollUnsupported()
	case fuseOpSetattr, fuseOpSymlink, fuseOpMknod, fuseOpMkdir, fuseOpUnlink, fuseOpRmdir, fuseOpRename,
		fuseOpRename2, fuseOpLink, fuseOpWrite, fuseOpCreate, fuseOpSetxattr, fuseOpRemovexattr:
		payload, errno = s.dispatchMutatingOp(st, opcode, nodeID, body)
	default:
		payload, errno = nil, -int32(syscall.ENOSYS)
	}
	_ = s.writeReply(st.unique, errno, payload)
}

func (s *Server) dispatchMutatingOp(st *fuseRequestState, opcode uint32, nodeID uint64, body []byte) ([]byte, int32) {
	if errno := s.gate.enter(st); errno != 0 {
		return nil, errno
	}
	defer s.gate.leave()

	rel, _ := s.nodes.resolveNodeRel(nodeID)
	if rel != "" {
		s.nodes.recordDirtyRel(rel)
	}
	if s.cfg.OnMutationHook != nil {
		s.cfg.OnMutationHook(opcode, rel)
	}

	switch opcode {
	case fuseOpSetattr:
		return s.opSetattr(nodeID, body)
	case fuseOpSymlink:
		return s.opSymlink(nodeID, body)
	case fuseOpMknod:
		return s.opMknod(nodeID, body)
	case fuseOpMkdir:
		return s.opMkdir(nodeID, body)
	case fuseOpUnlink:
		return s.opUnlink(nodeID, body)
	case fuseOpRmdir:
		return s.opRmdir(nodeID, body)
	case fuseOpRename:
		return s.opRename(nodeID, body, false)
	case fuseOpRename2:
		return s.opRename(nodeID, body, true)
	case fuseOpLink:
		return s.opLink(nodeID, body)
	case fuseOpWrite:
		return s.opWrite(body)
	case fuseOpCreate:
		return s.opCreate(nodeID, body)
	case fuseOpSetxattr:
		return s.opSetxattr(nodeID, body)
	default:
		return s.opRemovexattr(nodeID, body)
	}
}

// opInit pins the kernel ABI and grants only the wanted capabilities. Writeback caching and
// passthrough are never granted: they would let writes bypass first-mutation capture.
func (s *Server) opInit(body []byte) ([]byte, int32) {
	if len(body) < 16 {
		return nil, -int32(syscall.EINVAL)
	}
	kernelMajor := binary.LittleEndian.Uint32(body[0:4])
	kernelMinor := binary.LittleEndian.Uint32(body[4:8])
	if kernelMajor != FUSEProtocolMajor || kernelMinor < FUSEProtocolMinor {
		return nil, -int32(syscall.EPROTO)
	}
	maxReadahead := binary.LittleEndian.Uint32(body[8:12])
	granted := binary.LittleEndian.Uint32(body[12:16]) & uint32(fuseCapPOSIXLocks|fuseCapAtomicOTrunc|fuseCapBigWrites|fuseCapFlockLocks)
	s.negotiated.Store(&NegotiatedProtocol{KernelMajor: kernelMajor, KernelMinor: kernelMinor, GrantedFlags: granted})
	out := make([]byte, 64)
	binary.LittleEndian.PutUint32(out[0:4], FUSEProtocolMajor)
	binary.LittleEndian.PutUint32(out[4:8], FUSEProtocolMinor)
	binary.LittleEndian.PutUint32(out[8:12], maxReadahead)
	binary.LittleEndian.PutUint32(out[12:16], granted)
	binary.LittleEndian.PutUint16(out[16:18], 64)
	binary.LittleEndian.PutUint16(out[18:20], 48)
	binary.LittleEndian.PutUint32(out[20:24], DefaultMaxWrite)
	binary.LittleEndian.PutUint32(out[24:28], 1)
	binary.LittleEndian.PutUint16(out[28:30], 32)
	return out, 0
}

func (s *Server) opLookup(nodeID uint64, body []byte) ([]byte, int32) {
	parentRel, ok := s.nodes.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	name := cstr(body)
	if name == "" {
		return nil, -int32(syscall.ENOENT)
	}
	if s.pollProbeActive.Load() && nodeID == fuseRootID && name == pollProbeName {
		return encodeEntryOut(s, pollProbeNodeID, pollProbeAttr()), 0
	}
	childRel := joinRel(parentRel, name)
	attr, err := s.core.Lstat(s.qpath(childRel))
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	fn := s.nodes.ensureNode(childRel, attr)
	return encodeEntryOut(s, fn.fuseID, attr), 0
}

func (s *Server) opGetattr(nodeID uint64, body []byte) ([]byte, int32) {
	if nodeID == pollProbeNodeID {
		return encodeAttrOut(s, nodeID, pollProbeAttr()), 0
	}
	if rel, ok := s.nodes.resolveNodeRel(nodeID); ok {
		attr, err := s.core.Lstat(s.qpath(rel))
		if err != nil {
			return nil, mapFUSEErrno(err)
		}
		fn := s.nodes.ensureNode(rel, attr)
		return encodeAttrOut(s, fn.fuseID, attr), 0
	}
	var fh uint64
	if len(body) >= 16 {
		fh = binary.LittleEndian.Uint64(body[8:16])
	}
	if h, ok := s.nodes.lookupOpenHandleForNode(fh, nodeID); ok && h.hid != 0 {
		attr, err := s.core.StatHandle(h.hid)
		if err != nil {
			return nil, mapFUSEErrno(err)
		}
		return encodeAttrOut(s, nodeID, attr), 0
	}
	return nil, -int32(syscall.ENOENT)
}

func (s *Server) opSetattr(nodeID uint64, body []byte) ([]byte, int32) {
	if len(body) < 88 {
		return nil, -int32(syscall.EINVAL)
	}
	valid := binary.LittleEndian.Uint32(body[0:4])
	rel, ok := s.nodes.resolveNodeRel(nodeID)
	if !ok {
		// Core mutates attributes by path only, and a replaced or unlinked object has no path.
		// Its old name may now belong to another file, so the request must not reach it.
		if valid&fattrFH != 0 {
			return nil, -int32(syscall.ESTALE)
		}
		return nil, -int32(syscall.ENOENT)
	}
	req := layeredfs.SetAttrRequest{NoFollow: true}
	if valid&fattrUID != 0 {
		uid := binary.LittleEndian.Uint32(body[76:80])
		req.UID = &uid
	}
	if valid&fattrGID != 0 {
		gid := binary.LittleEndian.Uint32(body[80:84])
		req.GID = &gid
	}
	if valid&fattrSize != 0 {
		sz := int64(binary.LittleEndian.Uint64(body[16:24]))
		req.Size = &sz
	}
	if valid&fattrMode != 0 {
		mode := fromPOSIXMode(binary.LittleEndian.Uint32(body[68:72]))
		req.Mode = &mode
	}
	now := time.Now()
	if valid&(fattrAtime|fattrAtimeNow) != 0 {
		atime := now
		if valid&fattrAtime != 0 && valid&fattrAtimeNow == 0 {
			atime = time.Unix(int64(binary.LittleEndian.Uint64(body[32:40])), int64(binary.LittleEndian.Uint32(body[56:60])))
		}
		req.ATime = &atime
	}
	if valid&(fattrMtime|fattrMtimeNow) != 0 {
		mtime := now
		if valid&fattrMtime != 0 && valid&fattrMtimeNow == 0 {
			mtime = time.Unix(int64(binary.LittleEndian.Uint64(body[40:48])), int64(binary.LittleEndian.Uint32(body[60:64])))
		}
		req.MTime = &mtime
	}
	qp := s.qpath(rel)
	attr, err := s.core.SetAttr(qp, req)
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	fn := s.nodes.ensureNode(rel, attr)
	return encodeAttrOut(s, fn.fuseID, attr), 0
}

func (s *Server) opReadlink(nodeID uint64) ([]byte, int32) {
	rel, ok := s.nodes.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	target, err := s.core.Readlink(s.qpath(rel))
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	return []byte(target), 0
}

// opFsync acknowledges only what backing storage established. Linux fsync on any descriptor
// covers the file, but Core has nothing to sync for a read-only handle, so the private backing
// file is synced directly in that case.
func (s *Server) opFsync(body []byte) ([]byte, int32) {
	if len(body) < 8 {
		return nil, -int32(syscall.EINVAL)
	}
	h, ok := s.nodes.lookupHandle(binary.LittleEndian.Uint64(body[0:8]))
	if !ok || h.isDir || h.hid == 0 {
		return nil, -int32(syscall.EBADF)
	}
	if err := s.core.SyncHandle(h.hid); err != nil {
		return nil, mapFUSEErrno(err)
	}
	if h.writable {
		return nil, 0
	}
	attr, err := s.core.StatHandle(h.hid)
	if errors.Is(err, layeredfs.ErrStaleHandle) {
		return nil, 0 // SyncHandle validated the handle: it reads a lower layer with no private data.
	}
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	if attr.Layer != layeredfs.LayerPrivate || attr.NodeID == 0 {
		return nil, 0
	}
	backing, err := s.core.OpenBackingReader(attr.NodeID, attr.Generation)
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	defer func() { _ = backing.Close() }()
	file, ok := backing.(interface{ Sync() error })
	if !ok {
		return nil, -int32(syscall.EIO)
	}
	if err := s.syncBacking(file); err != nil {
		return nil, mapFUSEErrno(err)
	}
	return nil, 0
}

// opFsyncdir persists the journal, which is where the private namespace is durably recorded.
func (s *Server) opFsyncdir() ([]byte, int32) {
	if _, err := s.core.SnapshotJournal(); err != nil {
		return nil, mapFUSEErrno(err)
	}
	return nil, 0
}

// opStatfs reports the storage that private writes actually consume.
func (s *Server) opStatfs() ([]byte, int32) {
	var st unix.Statfs_t
	if err := unix.Statfs(s.cfg.BackingDir, &st); err != nil {
		return nil, mapFUSEErrno(err)
	}
	out := make([]byte, 80)
	binary.LittleEndian.PutUint64(out[0:8], st.Blocks)
	binary.LittleEndian.PutUint64(out[8:16], st.Bfree)
	binary.LittleEndian.PutUint64(out[16:24], st.Bavail)
	binary.LittleEndian.PutUint64(out[24:32], st.Files)
	binary.LittleEndian.PutUint64(out[32:40], st.Ffree)
	binary.LittleEndian.PutUint32(out[40:44], uint32(st.Bsize))
	binary.LittleEndian.PutUint32(out[44:48], uint32(st.Namelen))
	binary.LittleEndian.PutUint32(out[48:52], uint32(st.Frsize))
	return out, 0
}
