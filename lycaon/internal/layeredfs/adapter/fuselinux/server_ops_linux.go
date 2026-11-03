//go:build linux

package fuselinux

import (
	"encoding/binary"
	"errors"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

const (
	fuseOpLookup      = 1
	fuseOpForget      = 2
	fuseOpGetattr     = 3
	fuseOpSetattr     = 4
	fuseOpReadlink    = 5
	fuseOpSymlink     = 6
	fuseOpMknod       = 8
	fuseOpMkdir       = 9
	fuseOpUnlink      = 10
	fuseOpRmdir       = 11
	fuseOpRename      = 12
	fuseOpLink        = 13
	fuseOpOpen        = 14
	fuseOpRead        = 15
	fuseOpWrite       = 16
	fuseOpStatfs      = 17
	fuseOpRelease     = 18
	fuseOpFsync       = 20
	fuseOpSetxattr    = 21
	fuseOpGetxattr    = 22
	fuseOpListxattr   = 23
	fuseOpRemovexattr = 24
	fuseOpFlush       = 25
	fuseOpInit        = 26
	fuseOpOpendir     = 27
	fuseOpReaddir     = 28
	fuseOpReleasedir  = 29
	fuseOpFsyncdir    = 30
	fuseOpGetlk       = 31
	fuseOpSetlk       = 32
	fuseOpSetlkw      = 33
	fuseOpAccess      = 34
	fuseOpCreate      = 35
	fuseOpBatchForget = 42
	fuseOpRename2     = 45
	fuseOpLseek       = 46

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

func (s *Server) handleRequest(req []byte) {
	opcode := binary.LittleEndian.Uint32(req[4:8])
	unique := binary.LittleEndian.Uint64(req[8:16])
	nodeID := binary.LittleEndian.Uint64(req[16:24])
	body := req[fuseHeaderInSize:]

	if opcode == fuseOpForget || opcode == fuseOpBatchForget {
		return
	}

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
		payload, errno = s.opOpen(nodeID, body)
	case fuseOpRead:
		payload, errno = s.opRead(nodeID, body)
	case fuseOpStatfs:
		payload, errno = s.opStatfs()
	case fuseOpRelease:
		payload, errno = s.opRelease(body)
	case fuseOpFsync:
		payload, errno = s.opFsync(body)
	case fuseOpFsyncdir:
		payload, errno = nil, 0
	case fuseOpFlush:
		payload, errno = s.opFlush(body)
	case fuseOpOpendir:
		payload, errno = s.opOpendir(nodeID)
	case fuseOpReaddir:
		payload, errno = s.opReaddir(nodeID, body)
	case fuseOpReleasedir:
		payload, errno = s.opReleasedir(body)
	case fuseOpAccess:
		payload, errno = s.opAccess(nodeID)
	case fuseOpGetxattr:
		payload, errno = s.opGetxattr(nodeID, body)
	case fuseOpListxattr:
		payload, errno = s.opListxattr(nodeID, body)
	case fuseOpGetlk:
		payload, errno = s.opGetlk(nodeID, body)
	case fuseOpSetlk, fuseOpSetlkw:
		payload, errno = s.opSetlk(nodeID, body, opcode == fuseOpSetlkw)
	case fuseOpLseek:
		payload, errno = nil, -int32(syscall.ENOSYS)
	default:
		payload, errno = s.dispatchMutatingOp(opcode, nodeID, body)
	}
	_ = s.writeReply(unique, errno, payload)
}

func (s *Server) dispatchMutatingOp(opcode uint32, nodeID uint64, body []byte) ([]byte, int32) {
	if !s.enterMutation() {
		return nil, -int32(syscall.EAGAIN)
	}
	defer s.leaveMutation()

	rel, _ := s.resolveNodeRel(nodeID)
	if rel != "" {
		s.recordDirtyRel(rel)
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
		return s.opWrite(nodeID, body)
	case fuseOpCreate:
		return s.opCreate(nodeID, body)
	case fuseOpSetxattr:
		return s.opSetxattr(nodeID, body)
	case fuseOpRemovexattr:
		return s.opRemovexattr(nodeID, body)
	default:
		return nil, -int32(syscall.ENOSYS)
	}
}

func (s *Server) opInit(body []byte) ([]byte, int32) {
	var maxReadahead uint32 = DefaultMaxReadAhead
	var reqFlags uint32
	if len(body) >= 16 {
		maxReadahead = binary.LittleEndian.Uint32(body[8:12])
		reqFlags = binary.LittleEndian.Uint32(body[12:16])
	}
	wantFlags := uint32(fuseCapPOSIXLocks | fuseCapAtomicOTrunc | fuseCapBigWrites | fuseCapFlockLocks)
	out := make([]byte, 64)
	binary.LittleEndian.PutUint32(out[0:4], FUSEProtocolMajor)
	binary.LittleEndian.PutUint32(out[4:8], FUSEProtocolMinor)
	binary.LittleEndian.PutUint32(out[8:12], maxReadahead)
	binary.LittleEndian.PutUint32(out[12:16], reqFlags&wantFlags)
	binary.LittleEndian.PutUint16(out[16:18], 64)
	binary.LittleEndian.PutUint16(out[18:20], 48)
	binary.LittleEndian.PutUint32(out[20:24], DefaultMaxWrite)
	binary.LittleEndian.PutUint32(out[24:28], 1)
	binary.LittleEndian.PutUint16(out[28:30], 32)
	return out, 0
}

func (s *Server) opLookup(nodeID uint64, body []byte) ([]byte, int32) {
	parentRel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	name := cstr(body)
	if name == "" {
		return nil, -int32(syscall.ENOENT)
	}
	childRel := joinRel(parentRel, name)
	attr, err := s.core.Lstat(s.qpath(childRel))
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	fn := s.ensureNode(childRel, attr)
	return encodeEntryOut(s, fn.fuseID, attr), 0
}

func (s *Server) opGetattr(nodeID uint64, body []byte) ([]byte, int32) {
	if rel, ok := s.resolveNodeRel(nodeID); ok {
		attr, err := s.core.Lstat(s.qpath(rel))
		if err != nil {
			return nil, mapFUSEErrno(err)
		}
		fn := s.ensureNode(rel, attr)
		return encodeAttrOut(s, fn.fuseID, attr), 0
	}
	var fh uint64
	if len(body) >= 16 {
		fh = binary.LittleEndian.Uint64(body[8:16])
	}
	if h, ok := s.lookupOpenHandleForNode(fh, nodeID); ok && h.hid != 0 {
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
	fh := binary.LittleEndian.Uint64(body[8:16])
	rel, ok := s.resolveNodeRel(nodeID)
	if !ok && valid&fattrFH != 0 {
		if h, hok := s.lookupHandle(fh); hok && h.rel != "" {
			rel, ok = h.rel, true
		}
	}
	if !ok {
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
	fn := s.ensureNode(rel, attr)
	return encodeAttrOut(s, fn.fuseID, attr), 0
}

func (s *Server) opReadlink(nodeID uint64) ([]byte, int32) {
	rel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	target, err := s.core.Readlink(s.qpath(rel))
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	return []byte(target), 0
}

func (s *Server) opFsync(body []byte) ([]byte, int32) {
	if len(body) < 8 {
		return nil, 0
	}
	fh := binary.LittleEndian.Uint64(body[0:8])
	if h, ok := s.lookupHandle(fh); ok && !h.isDir && h.hid != 0 {
		if err := s.core.SyncHandle(h.hid); err != nil && !errors.Is(err, layeredfs.ErrStaleHandle) {
			return nil, mapFUSEErrno(err)
		}
	}
	return nil, 0
}

func (s *Server) opStatfs() ([]byte, int32) {
	out := make([]byte, 80)
	binary.LittleEndian.PutUint64(out[0:8], 1024*1024)
	binary.LittleEndian.PutUint64(out[8:16], 1024*1024)
	binary.LittleEndian.PutUint64(out[16:24], 1024*1024)
	binary.LittleEndian.PutUint64(out[24:32], 1024*1024)
	binary.LittleEndian.PutUint64(out[32:40], 1024*1024)
	binary.LittleEndian.PutUint32(out[40:44], 4096)
	binary.LittleEndian.PutUint32(out[44:48], 255)
	binary.LittleEndian.PutUint32(out[48:52], 4096)
	return out, 0
}

func (s *Server) opAccess(_ uint64) ([]byte, int32) {
	// Returning ENOSYS instructs Linux FUSE (fc->no_access = 1) to evaluate permissions in VFS
	// from fuse_attr without round-tripping FUSE_ACCESS during vfork+chdir.
	return nil, -int32(syscall.ENOSYS)
}
