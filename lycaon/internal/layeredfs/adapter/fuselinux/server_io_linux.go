//go:build linux

package fuselinux

import (
	"encoding/binary"
	"io"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

const fuseWriteInHeaderSize = 40

func (s *Server) storeOpenHandle(fh uint64, entry openHandleEntry) {
	s.nodeMu.Lock()
	s.openHandles[fh] = entry
	if entry.rel != "" {
		s.dirtyRels[entry.rel] = struct{}{}
	}
	s.nodeMu.Unlock()
}

func (s *Server) lookupHandle(fh uint64) (openHandleEntry, bool) {
	s.nodeMu.RLock()
	h, ok := s.openHandles[fh]
	s.nodeMu.RUnlock()
	return h, ok
}

func (s *Server) lookupOpenHandleForNode(fh, nodeID uint64) (openHandleEntry, bool) {
	s.nodeMu.RLock()
	defer s.nodeMu.RUnlock()
	if fh != 0 {
		if h, ok := s.openHandles[fh]; ok {
			return h, true
		}
	}
	for _, h := range s.openHandles {
		if h.fuseID == nodeID && !h.isDir && h.hid != 0 {
			return h, true
		}
	}
	return openHandleEntry{}, false
}

func (s *Server) opOpen(nodeID uint64, body []byte) ([]byte, int32) {
	if len(body) < 8 {
		return nil, -int32(syscall.EINVAL)
	}
	rel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	rawFlags := binary.LittleEndian.Uint32(body[0:4])
	openFlags := translateOpenFlags(rawFlags)
	mutating := openFlags != layeredfs.OpenReadOnly
	if mutating {
		if !s.enterMutation() {
			return nil, -int32(syscall.EAGAIN)
		}
		defer s.leaveMutation()
		if s.cfg.OnMutationHook != nil {
			s.cfg.OnMutationHook(fuseOpOpen, rel)
		}
	}
	qp := s.qpath(rel)
	hid, attr, err := s.core.Open(qp, openFlags, 0o644)
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	fn := s.ensureNode(rel, attr)
	fh := uint64(hid)
	s.storeOpenHandle(fh, openHandleEntry{
		hid:    hid,
		fuseID: fn.fuseID,
		rel:    cleanFuseRel(rel),
		isDir:  false,
	})
	out := make([]byte, 16)
	binary.LittleEndian.PutUint64(out[0:8], fh)
	return out, 0
}

func (s *Server) opCreate(nodeID uint64, body []byte) ([]byte, int32) {
	if len(body) < 16 {
		return nil, -int32(syscall.EINVAL)
	}
	parentRel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	rawFlags := binary.LittleEndian.Uint32(body[0:4])
	rawMode := binary.LittleEndian.Uint32(body[4:8])
	name := cstr(body[16:])
	if name == "" {
		return nil, -int32(syscall.EINVAL)
	}
	childRel := joinRel(parentRel, name)
	qp := s.qpath(childRel)
	openFlags := translateOpenFlags(rawFlags) | layeredfs.OpenCreate
	if openFlags&(layeredfs.OpenWriteOnly|layeredfs.OpenReadWrite) == 0 {
		openFlags |= layeredfs.OpenReadWrite
	}
	hid, attr, err := s.core.Open(qp, openFlags, fromPOSIXMode(rawMode))
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	fn := s.ensureNode(childRel, attr)
	fh := uint64(hid)
	s.storeOpenHandle(fh, openHandleEntry{
		hid:    hid,
		fuseID: fn.fuseID,
		rel:    cleanFuseRel(childRel),
		isDir:  false,
	})
	entryBytes := encodeEntryOut(s, fn.fuseID, attr)
	out := make([]byte, len(entryBytes)+16)
	copy(out, entryBytes)
	binary.LittleEndian.PutUint64(out[len(entryBytes):len(entryBytes)+8], fh)
	return out, 0
}

func (s *Server) opRead(nodeID uint64, body []byte) ([]byte, int32) {
	if len(body) < 24 {
		return nil, -int32(syscall.EINVAL)
	}
	fh := binary.LittleEndian.Uint64(body[0:8])
	offset := int64(binary.LittleEndian.Uint64(body[8:16]))
	size := int(binary.LittleEndian.Uint32(body[16:20]))
	if size <= 0 {
		return nil, 0
	}
	buf := make([]byte, size)
	if h, ok := s.lookupOpenHandleForNode(fh, nodeID); ok && h.hid != 0 {
		n, err := s.core.ReadHandle(h.hid, buf, offset)
		if err == nil || err == io.EOF {
			return buf[:n], 0
		}
	}
	rel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	hid, _, err := s.core.Open(s.qpath(rel), layeredfs.OpenReadOnly, 0)
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	n, rerr := s.core.ReadHandle(hid, buf, offset)
	_ = s.core.CloseHandle(hid)
	if rerr != nil && rerr != io.EOF {
		return nil, mapFUSEErrno(rerr)
	}
	return buf[:n], 0
}

func (s *Server) opWrite(nodeID uint64, body []byte) ([]byte, int32) {
	if len(body) < fuseWriteInHeaderSize {
		return nil, -int32(syscall.EINVAL)
	}
	fh := binary.LittleEndian.Uint64(body[0:8])
	offset := int64(binary.LittleEndian.Uint64(body[8:16]))
	size := int(binary.LittleEndian.Uint32(body[16:20]))
	if len(body) < fuseWriteInHeaderSize+size {
		size = len(body) - fuseWriteInHeaderSize
	}
	data := body[fuseWriteInHeaderSize : fuseWriteInHeaderSize+size]
	if h, ok := s.lookupOpenHandleForNode(fh, nodeID); ok && h.hid != 0 {
		if h.rel != "" {
			s.recordDirtyRel(h.rel)
		}
		n, err := s.core.WriteHandle(h.hid, data, offset)
		if err == nil {
			out := make([]byte, 8)
			binary.LittleEndian.PutUint32(out[0:4], uint32(n))
			return out, 0
		}
	}
	rel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	qp := s.qpath(rel)
	hid, attr, err := s.core.Open(qp, layeredfs.OpenReadWrite, 0)
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	n, werr := s.core.WriteHandle(hid, data, offset)
	_ = s.core.CloseHandle(hid)
	if werr != nil {
		return nil, mapFUSEErrno(werr)
	}
	s.ensureNode(rel, attr)
	out := make([]byte, 8)
	binary.LittleEndian.PutUint32(out[0:4], uint32(n))
	return out, 0
}

func (s *Server) opFlush(body []byte) ([]byte, int32) {
	if len(body) < 8 {
		return nil, -int32(syscall.EINVAL)
	}
	return nil, 0
}

func (s *Server) opRelease(body []byte) ([]byte, int32) {
	if len(body) < 8 {
		return nil, -int32(syscall.EINVAL)
	}
	fh := binary.LittleEndian.Uint64(body[0:8])
	s.nodeMu.Lock()
	h, ok := s.openHandles[fh]
	delete(s.openHandles, fh)
	s.nodeMu.Unlock()
	s.releaseLocksForHandle(fh, h.fuseID)
	if ok && !h.isDir && h.hid != 0 {
		_ = s.core.CloseHandle(h.hid)
	}
	return nil, 0
}

func (s *Server) opOpendir(nodeID uint64) ([]byte, int32) {
	rel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	attr, err := s.core.Lstat(s.qpath(rel))
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	if !attr.Mode.IsDir() {
		return nil, -int32(syscall.ENOTDIR)
	}
	s.nodeMu.Lock()
	fh := s.nextDirFH
	s.nextDirFH++
	s.openHandles[fh] = openHandleEntry{
		fuseID: nodeID,
		rel:    cleanFuseRel(rel),
		isDir:  true,
	}
	s.nodeMu.Unlock()
	out := make([]byte, 16)
	binary.LittleEndian.PutUint64(out[0:8], fh)
	return out, 0
}

func (s *Server) opReaddir(nodeID uint64, body []byte) ([]byte, int32) {
	if len(body) < 24 {
		return nil, -int32(syscall.EINVAL)
	}
	fh := binary.LittleEndian.Uint64(body[0:8])
	offset := int(binary.LittleEndian.Uint64(body[8:16]))
	maxSize := int(binary.LittleEndian.Uint32(body[16:20]))
	rel, ok := s.resolveNodeRel(nodeID)
	if !ok {
		if h, hok := s.lookupHandle(fh); hok && h.rel != "" {
			rel, ok = h.rel, true
		}
	}
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	entries, err := s.core.ReadDir(s.qpath(rel))
	if err != nil {
		return nil, mapFUSEErrno(err)
	}
	type rawDirent struct {
		ino  uint64
		typ  uint32
		name string
	}
	all := make([]rawDirent, 0, len(entries)+2)
	all = append(all,
		rawDirent{ino: nodeID, typ: syscall.DT_DIR, name: "."},
		rawDirent{ino: fuseRootID, typ: syscall.DT_DIR, name: ".."},
	)
	for _, e := range entries {
		childRel := joinRel(rel, e.Name)
		fn := s.ensureNode(childRel, layeredfs.EntryAttr{
			NodeID:     e.NodeID,
			Generation: e.Generation,
			Layer:      e.Layer,
			Kind:       e.Kind,
			Mode:       e.Mode,
			Size:       e.Size,
			ModTime:    e.ModTime,
		})
		all = append(all, rawDirent{
			ino:  fn.fuseID,
			typ:  direntType(e.Mode),
			name: e.Name,
		})
	}
	if offset < 0 || offset >= len(all) {
		return nil, 0
	}
	var out []byte
	for idx := offset; idx < len(all); idx++ {
		d := all[idx]
		nameLen := len(d.name)
		recLen := (24 + nameLen + 7) &^ 7
		if len(out)+recLen > maxSize {
			break
		}
		rec := make([]byte, recLen)
		binary.LittleEndian.PutUint64(rec[0:8], d.ino)
		binary.LittleEndian.PutUint64(rec[8:16], uint64(idx+1))
		binary.LittleEndian.PutUint32(rec[16:20], uint32(nameLen))
		binary.LittleEndian.PutUint32(rec[20:24], d.typ)
		copy(rec[24:], d.name)
		out = append(out, rec...)
	}
	return out, 0
}

func (s *Server) opReleasedir(body []byte) ([]byte, int32) {
	if len(body) < 8 {
		return nil, -int32(syscall.EINVAL)
	}
	fh := binary.LittleEndian.Uint64(body[0:8])
	s.nodeMu.Lock()
	delete(s.openHandles, fh)
	s.nodeMu.Unlock()
	return nil, 0
}
