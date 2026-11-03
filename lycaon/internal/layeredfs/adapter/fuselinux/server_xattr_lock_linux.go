//go:build linux

package fuselinux

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"golang.org/x/sys/unix"
)

func (s *Server) opSetxattr(nodeID uint64, body []byte) ([]byte, int32) {
	if len(body) < 8 {
		return nil, -int32(syscall.EINVAL)
	}
	rel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	valSize := int(binary.LittleEndian.Uint32(body[0:4]))
	flags := int(binary.LittleEndian.Uint32(body[4:8]))
	rest := body[8:]
	nulIdx := bytes.IndexByte(rest, 0)
	if nulIdx <= 0 {
		return nil, -int32(syscall.EINVAL)
	}
	name := string(rest[:nulIdx])
	if isUnsupportedSystemXattr(name) {
		return nil, -int32(syscall.EOPNOTSUPP)
	}
	valBytes := rest[nulIdx+1:]
	if len(valBytes) > valSize {
		valBytes = valBytes[:valSize]
	}
	qp := s.qpath(rel)
	if flags&(unix.XATTR_CREATE|unix.XATTR_REPLACE) != 0 {
		_, getErr := s.core.GetXattr(qp, name, true)
		switch {
		case getErr == nil && flags&unix.XATTR_CREATE != 0:
			return nil, -int32(syscall.EEXIST)
		case errors.Is(getErr, syscall.ENODATA) && flags&unix.XATTR_REPLACE != 0:
			return nil, -int32(syscall.ENODATA)
		case getErr != nil && !errors.Is(getErr, syscall.ENODATA):
			return nil, mapFUSEErrno(getErr)
		}
	}
	if err := s.core.SetXattr(qp, name, valBytes, true); err != nil {
		return nil, mapFUSEErrno(err)
	}
	if attr, lerr := s.core.Lstat(qp); lerr == nil {
		s.ensureNode(rel, attr)
	}
	return nil, 0
}

func (s *Server) opGetxattr(nodeID uint64, body []byte) ([]byte, int32) {
	if len(body) < 8 {
		return nil, -int32(syscall.EINVAL)
	}
	rel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	maxSize := int(binary.LittleEndian.Uint32(body[0:4]))
	name := cstr(body[8:])
	if name == "" {
		return nil, -int32(syscall.EINVAL)
	}
	if isUnsupportedSystemXattr(name) {
		return nil, -int32(syscall.EOPNOTSUPP)
	}
	if name == "security.capability" || name == "security.selinux" {
		return nil, -int32(syscall.ENODATA)
	}
	val, err := s.core.GetXattr(s.qpath(rel), name, true)
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	if maxSize == 0 {
		out := make([]byte, 8)
		binary.LittleEndian.PutUint32(out[0:4], uint32(len(val)))
		return out, 0
	}
	if maxSize < len(val) {
		return nil, -int32(syscall.ERANGE)
	}
	return val, 0
}

func (s *Server) opListxattr(nodeID uint64, body []byte) ([]byte, int32) {
	if len(body) < 8 {
		return nil, -int32(syscall.EINVAL)
	}
	rel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	maxSize := int(binary.LittleEndian.Uint32(body[0:4]))
	names, err := s.core.ListXattr(s.qpath(rel), true)
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	var buf []byte
	for _, n := range names {
		buf = append(buf, n...)
		buf = append(buf, 0)
	}
	if maxSize == 0 {
		out := make([]byte, 8)
		binary.LittleEndian.PutUint32(out[0:4], uint32(len(buf)))
		return out, 0
	}
	if maxSize < len(buf) {
		return nil, -int32(syscall.ERANGE)
	}
	return buf, 0
}

func (s *Server) opRemovexattr(nodeID uint64, body []byte) ([]byte, int32) {
	rel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	name := cstr(body)
	if name == "" {
		return nil, -int32(syscall.EINVAL)
	}
	if isUnsupportedSystemXattr(name) {
		return nil, -int32(syscall.EOPNOTSUPP)
	}
	qp := s.qpath(rel)
	if _, err := s.core.GetXattr(qp, name, true); err != nil {
		return nil, mapFUSEErrno(err)
	}
	if err := s.core.RemoveXattr(qp, name, true); err != nil {
		return nil, mapFUSEErrno(err)
	}
	if attr, lerr := s.core.Lstat(qp); lerr == nil {
		s.ensureNode(rel, attr)
	}
	return nil, 0
}

func isUnsupportedSystemXattr(name string) bool {
	return name == "system.posix_acl_access" || name == "system.posix_acl_default"
}

func encodeEntryOut(s *Server, nodeID uint64, attr layeredfs.EntryAttr) []byte {
	out := make([]byte, 128)
	binary.LittleEndian.PutUint64(out[0:8], nodeID)
	binary.LittleEndian.PutUint64(out[8:16], 1)
	fillFuseAttr(s, out[40:128], nodeID, attr)
	return out
}

func encodeAttrOut(s *Server, nodeID uint64, attr layeredfs.EntryAttr) []byte {
	out := make([]byte, 104)
	if nodeID == fuseRootID {
		// Cache the immutable mount root directory attributes in VFS so vfork+chdir(mountPath)
		// before execve never blocks waiting for a user-space round-trip.
		binary.LittleEndian.PutUint64(out[0:8], 3600)
	}
	fillFuseAttr(s, out[16:104], nodeID, attr)
	return out
}

func fillFuseAttr(s *Server, dst []byte, ino uint64, attr layeredfs.EntryAttr) {
	size := uint64(0)
	if attr.Size > 0 {
		size = uint64(attr.Size)
	}
	blocks := (size + 511) / 512
	atime := attr.ATime
	if atime.IsZero() {
		atime = attr.ModTime
	}
	ctime := attr.CTime
	if ctime.IsZero() {
		ctime = attr.ModTime
	}
	nlink := attr.Nlink
	if nlink == 0 {
		nlink = 1
	}
	binary.LittleEndian.PutUint64(dst[0:8], ino)
	binary.LittleEndian.PutUint64(dst[8:16], size)
	binary.LittleEndian.PutUint64(dst[16:24], blocks)
	binary.LittleEndian.PutUint64(dst[24:32], uint64(atime.Unix()))
	binary.LittleEndian.PutUint64(dst[32:40], uint64(attr.ModTime.Unix()))
	binary.LittleEndian.PutUint64(dst[40:48], uint64(ctime.Unix()))
	binary.LittleEndian.PutUint32(dst[48:52], uint32(atime.Nanosecond()))
	binary.LittleEndian.PutUint32(dst[52:56], uint32(attr.ModTime.Nanosecond()))
	binary.LittleEndian.PutUint32(dst[56:60], uint32(ctime.Nanosecond()))
	binary.LittleEndian.PutUint32(dst[60:64], toPOSIXMode(attr.Mode))
	binary.LittleEndian.PutUint32(dst[64:68], nlink)
	binary.LittleEndian.PutUint32(dst[68:72], s.cfg.OwnerUID)
	binary.LittleEndian.PutUint32(dst[72:76], s.cfg.OwnerGID)
	binary.LittleEndian.PutUint32(dst[80:84], 4096)
}

func toPOSIXMode(m fs.FileMode) uint32 {
	perm := uint32(m.Perm())
	if m&fs.ModeSetuid != 0 {
		perm |= syscall.S_ISUID
	}
	if m&fs.ModeSetgid != 0 {
		perm |= syscall.S_ISGID
	}
	if m&fs.ModeSticky != 0 {
		perm |= syscall.S_ISVTX
	}
	switch {
	case m.IsDir():
		return syscall.S_IFDIR | perm
	case m&fs.ModeSymlink != 0:
		return syscall.S_IFLNK | 0o777
	default:
		return syscall.S_IFREG | perm
	}
}

func fromPOSIXMode(raw uint32) fs.FileMode {
	mode := fs.FileMode(raw & 0o777)
	if raw&syscall.S_ISUID != 0 {
		mode |= fs.ModeSetuid
	}
	if raw&syscall.S_ISGID != 0 {
		mode |= fs.ModeSetgid
	}
	if raw&syscall.S_ISVTX != 0 {
		mode |= fs.ModeSticky
	}
	return mode
}
