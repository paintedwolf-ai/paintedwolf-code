//go:build linux

package fuselinux

import (
	"bytes"
	"encoding/binary"
	"io/fs"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"golang.org/x/sys/unix"
)

func (s *Server) opSymlink(nodeID uint64, body []byte) ([]byte, int32) {
	parentRel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	name, target := twoCStrings(body)
	if name == "" || target == "" {
		return nil, -int32(syscall.EINVAL)
	}
	childRel := joinRel(parentRel, name)
	attr, err := s.core.Symlink(target, s.qpath(childRel))
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	fn := s.ensureNode(childRel, attr)
	return encodeEntryOut(s, fn.fuseID, attr), 0
}

func (s *Server) opMknod(nodeID uint64, body []byte) ([]byte, int32) {
	if len(body) < 16 {
		return nil, -int32(syscall.EINVAL)
	}
	parentRel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	rawMode := binary.LittleEndian.Uint32(body[0:4])
	fileType := rawMode & syscall.S_IFMT
	if fileType != 0 && fileType != syscall.S_IFREG {
		return nil, -int32(syscall.EPERM)
	}
	name := cstr(body[16:])
	if name == "" {
		return nil, -int32(syscall.EINVAL)
	}
	childRel := joinRel(parentRel, name)
	qp := s.qpath(childRel)
	hid, attr, err := s.core.Open(qp, layeredfs.OpenCreate|layeredfs.OpenTruncate|layeredfs.OpenReadWrite, fromPOSIXMode(rawMode))
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	_ = s.core.CloseHandle(hid)
	fn := s.ensureNode(childRel, attr)
	return encodeEntryOut(s, fn.fuseID, attr), 0
}

func (s *Server) opMkdir(nodeID uint64, body []byte) ([]byte, int32) {
	if len(body) < 8 {
		return nil, -int32(syscall.EINVAL)
	}
	parentRel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	rawMode := binary.LittleEndian.Uint32(body[0:4])
	name := cstr(body[8:])
	if name == "" {
		return nil, -int32(syscall.EINVAL)
	}
	childRel := joinRel(parentRel, name)
	attr, err := s.core.Mkdir(s.qpath(childRel), fromPOSIXMode(rawMode))
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	s.recordDirtyRel(childRel)
	fn := s.ensureNode(childRel, attr)
	return encodeEntryOut(s, fn.fuseID, attr), 0
}

func (s *Server) opUnlink(nodeID uint64, body []byte) ([]byte, int32) {
	parentRel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	name := cstr(body)
	if name == "" {
		return nil, -int32(syscall.EINVAL)
	}
	childRel := joinRel(parentRel, name)
	qp := s.qpath(childRel)
	prevAttr, prevErr := s.core.Lstat(qp)
	if err := s.core.Unlink(qp, false); err != nil {
		return nil, mapFUSEErrno(err)
	}
	s.recordUnlink(childRel, prevAttr, prevErr == nil)
	return nil, 0
}

func (s *Server) opRmdir(nodeID uint64, body []byte) ([]byte, int32) {
	parentRel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	name := cstr(body)
	if name == "" {
		return nil, -int32(syscall.EINVAL)
	}
	childRel := joinRel(parentRel, name)
	if err := s.core.Unlink(s.qpath(childRel), true); err != nil {
		return nil, mapFUSEErrno(err)
	}
	s.recordUnlink(childRel, layeredfs.EntryAttr{}, false)
	return nil, 0
}

func (s *Server) opRename(nodeID uint64, body []byte, isRename2 bool) ([]byte, int32) {
	headerLen := 8
	var flags uint32
	if isRename2 {
		headerLen = 16
	}
	if len(body) < headerLen {
		return nil, -int32(syscall.EINVAL)
	}
	newDirID := binary.LittleEndian.Uint64(body[0:8])
	if isRename2 {
		flags = binary.LittleEndian.Uint32(body[8:12])
		if flags&^uint32(unix.RENAME_NOREPLACE) != 0 {
			return nil, -int32(syscall.EINVAL)
		}
	}
	oldParent, ok1 := s.resolveNodeRel(nodeID)
	newParent, ok2 := s.resolveNodeRel(newDirID)
	if !ok1 || !ok2 {
		return nil, -int32(syscall.ENOENT)
	}
	oldName, newName := twoCStrings(body[headerLen:])
	if oldName == "" || newName == "" {
		return nil, -int32(syscall.EINVAL)
	}
	oldRel := joinRel(oldParent, oldName)
	newRel := joinRel(newParent, newName)
	if flags&uint32(unix.RENAME_NOREPLACE) != 0 {
		if _, err := s.core.Lstat(s.qpath(newRel)); err == nil {
			return nil, -int32(syscall.EEXIST)
		}
	}
	if err := s.core.Rename(s.qpath(oldRel), s.qpath(newRel)); err != nil {
		return nil, mapFUSEErrno(err)
	}
	s.recordRename(oldRel, newRel)
	return nil, 0
}

func (s *Server) opLink(nodeID uint64, body []byte) ([]byte, int32) {
	if len(body) < 8 {
		return nil, -int32(syscall.EINVAL)
	}
	oldNodeID := binary.LittleEndian.Uint64(body[0:8])
	oldRel, ok1 := s.resolveNodeRel(oldNodeID)
	parentRel, ok2 := s.resolveNodeRel(nodeID)
	if !ok1 || !ok2 {
		return nil, -int32(syscall.ENOENT)
	}
	newName := cstr(body[8:])
	if newName == "" {
		return nil, -int32(syscall.EINVAL)
	}
	newRel := joinRel(parentRel, newName)
	attr, err := s.core.Link(s.qpath(oldRel), s.qpath(newRel))
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	s.recordDirtyRel(oldRel)
	s.recordDirtyRel(newRel)
	s.ensureNode(oldRel, attr)
	fn := s.ensureNode(newRel, attr)
	return encodeEntryOut(s, fn.fuseID, attr), 0
}

func direntType(m fs.FileMode) uint32 {
	switch {
	case m.IsDir():
		return syscall.DT_DIR
	case m&fs.ModeSymlink != 0:
		return syscall.DT_LNK
	default:
		return syscall.DT_REG
	}
}

func translateOpenFlags(raw uint32) layeredfs.OpenFlags {
	var out layeredfs.OpenFlags
	switch raw & uint32(syscall.O_ACCMODE) {
	case syscall.O_WRONLY:
		out |= layeredfs.OpenWriteOnly
	case syscall.O_RDWR:
		out |= layeredfs.OpenReadWrite
	default:
		out |= layeredfs.OpenReadOnly
	}
	if raw&uint32(syscall.O_CREAT) != 0 {
		out |= layeredfs.OpenCreate
	}
	if raw&uint32(syscall.O_EXCL) != 0 {
		out |= layeredfs.OpenExclusive
	}
	if raw&uint32(syscall.O_TRUNC) != 0 {
		out |= layeredfs.OpenTruncate
	}
	if raw&uint32(syscall.O_APPEND) != 0 {
		out |= layeredfs.OpenAppend
	}
	return out
}

func cstr(b []byte) string {
	if idx := bytes.IndexByte(b, 0); idx >= 0 {
		return string(b[:idx])
	}
	return string(b)
}

func twoCStrings(b []byte) (string, string) {
	idx1 := bytes.IndexByte(b, 0)
	if idx1 < 0 {
		return "", ""
	}
	first := string(b[:idx1])
	rest := b[idx1+1:]
	idx2 := bytes.IndexByte(rest, 0)
	if idx2 < 0 {
		return first, string(rest)
	}
	return first, string(rest[:idx2])
}

func mapFUSEErrno(err error) int32 {
	if err == nil {
		return 0
	}
	return -int32(layeredfs.MapErrno(err))
}
