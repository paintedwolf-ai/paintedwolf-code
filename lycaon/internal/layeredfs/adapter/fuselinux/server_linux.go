//go:build linux

package fuselinux

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"golang.org/x/sys/unix"
)

const (
	fuseHeaderOutSize = 16
	fuseMaxReadBuffer = 1024 * 1024
)

func mountAndStartFUSEServer(ctx context.Context, adapterCfg Config, srvCfg ServerConfig, core *layeredfs.Core) (*Server, error) {
	fuseFD, err := receiveFUSEDevFD(ctx, adapterCfg.FusermountBin, srvCfg.MountOptions, srvCfg.MountPath)
	if err != nil {
		rc := adapter.ReasonFUSEMountDenied
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			rc = adapter.ReasonContextCanceled
		}
		return nil, &adapter.AdapterError{
			AdapterID:  adapter.AdapterFUSELinux,
			Op:         "prepare",
			ReasonCode: rc,
			Diagnostic: err.Error(),
			Cause:      err,
		}
	}
	var pipeFDs [2]int
	if err := unix.Pipe2(pipeFDs[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		_ = unix.Close(fuseFD)
		return nil, &adapter.AdapterError{
			AdapterID:  adapter.AdapterFUSELinux,
			Op:         "prepare",
			ReasonCode: adapter.ReasonFUSEMountDenied,
			Diagnostic: fmt.Sprintf("pipe2: %v", err),
			Cause:      err,
		}
	}
	srv := newServerInstance(srvCfg, core, fuseFD, pipeFDs)
	srv.wg.Add(1)
	go srv.serveLoop()
	return srv, nil
}

func receiveFUSEDevFD(ctx context.Context, fusermountBin, opts, mountPath string) (int, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("fuselinux: socketpair: %w", err)
	}
	parentSock := fds[0]
	childSockFile := os.NewFile(uintptr(fds[1]), "fuse-comm-child")
	defer func() {
		_ = unix.Close(parentSock)
		if childSockFile != nil {
			_ = childSockFile.Close()
		}
	}()

	cmd := exec.CommandContext(ctx, fusermountBin, "-o", opts, "--", mountPath)
	cmd.Env = append(os.Environ(), "_FUSE_COMMFD=3")
	cmd.ExtraFiles = []*os.File{childSockFile}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("fuselinux: start %s: %w", fusermountBin, err)
	}
	_ = childSockFile.Close()
	childSockFile = nil

	buf := make([]byte, 64)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, recvErr := unix.Recvmsg(parentSock, buf, oob, unix.MSG_CMSG_CLOEXEC)
	waitErr := cmd.Wait()
	if recvErr != nil {
		return -1, fmt.Errorf("fuselinux: recvmsg from %s: %w (wait=%v stderr=%s)", fusermountBin, recvErr, waitErr, strings.TrimSpace(stderr.String()))
	}
	if oobn == 0 {
		return -1, fmt.Errorf("fuselinux: %s exited without passing /dev/fuse fd (wait=%v stderr=%s)", fusermountBin, waitErr, strings.TrimSpace(stderr.String()))
	}
	scms, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(scms) == 0 {
		return -1, fmt.Errorf("fuselinux: parse control message: %w (stderr=%s)", err, strings.TrimSpace(stderr.String()))
	}
	gotFDs, err := unix.ParseUnixRights(&scms[0])
	if err != nil || len(gotFDs) == 0 {
		return -1, fmt.Errorf("fuselinux: parse unix rights: %w (stderr=%s)", err, strings.TrimSpace(stderr.String()))
	}
	fuseFD := gotFDs[0]
	for _, extra := range gotFDs[1:] {
		_ = unix.Close(extra)
	}
	unix.CloseOnExec(fuseFD)
	return fuseFD, nil
}

func (s *Server) serveLoop() {
	defer func() {
		s.closed.Store(true)
		s.gateMu.Lock()
		s.gateCond.Broadcast()
		s.gateMu.Unlock()
		s.lockMu.Lock()
		s.lockCond.Broadcast()
		s.lockMu.Unlock()
		s.wg.Done()
	}()

	buf := make([]byte, fuseMaxReadBuffer)
	pollFDs := []unix.PollFd{
		{Fd: int32(s.fd), Events: unix.POLLIN},
		{Fd: int32(s.stopPipe[0]), Events: unix.POLLIN | unix.POLLHUP},
	}
	for {
		_, err := unix.Poll(pollFDs, -1)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return
		}
		if pollFDs[1].Revents != 0 || s.closed.Load() {
			return
		}
		if pollFDs[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 && pollFDs[0].Revents&unix.POLLIN == 0 {
			return
		}
		if pollFDs[0].Revents&unix.POLLIN == 0 {
			continue
		}
		n, err := unix.Read(s.fd, buf)
		if err != nil {
			if errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.ENOENT) {
				continue
			}
			return
		}
		if n < fuseHeaderInSize {
			continue
		}
		req := make([]byte, n)
		copy(req, buf[:n])
		dispatchIncomingRequest(s, req)
	}
}

func (s *Server) writeReply(unique uint64, errno int32, payload []byte) error {
	total := fuseHeaderOutSize + len(payload)
	out := make([]byte, total)
	binary.LittleEndian.PutUint32(out[0:4], uint32(total))
	binary.LittleEndian.PutUint32(out[4:8], uint32(errno))
	binary.LittleEndian.PutUint64(out[8:16], unique)
	if len(payload) > 0 {
		copy(out[16:], payload)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed.Load() || s.fd < 0 {
		return syscall.EBADF
	}
	_, err := unix.Write(s.fd, out)
	return err
}

func notifyInvalInode(s *Server, ino uint64) {
	if !s.IsAlive() || ino == 0 {
		return
	}
	var payload [24]byte
	binary.LittleEndian.PutUint64(payload[0:8], ino)
	binary.LittleEndian.PutUint64(payload[8:16], 0)
	binary.LittleEndian.PutUint64(payload[16:24], ^uint64(0))
	_ = s.writeReply(0, 2, payload[:])
}

func notifyInvalEntry(s *Server, parent uint64, name string) {
	if !s.IsAlive() || parent == 0 || name == "" {
		return
	}
	payload := make([]byte, 16+len(name)+1)
	binary.LittleEndian.PutUint64(payload[0:8], parent)
	binary.LittleEndian.PutUint32(payload[8:12], uint32(len(name)))
	copy(payload[16:], name)
	_ = s.writeReply(0, 3, payload)
}

func (s *Server) qpath(rel string) layeredfs.QualifiedPath {
	return layeredfs.QualifiedPath{RootID: s.cfg.RootID, Rel: cleanFuseRel(rel)}
}

func joinRel(parentRel, name string) string {
	parentClean := cleanFuseRel(parentRel)
	if name == "" || name == "." {
		return parentClean
	}
	if parentClean == "." {
		return cleanFuseRel(name)
	}
	return cleanFuseRel(parentClean + "/" + name)
}

func (s *Server) resolveNodeRel(nodeID uint64) (string, bool) {
	if nodeID == fuseRootID {
		return ".", true
	}
	s.nodeMu.RLock()
	defer s.nodeMu.RUnlock()
	if fn, ok := s.byFuseID[nodeID]; ok {
		return fn.primaryRel()
	}
	return "", false
}

func (s *Server) ensureNode(rel string, attr layeredfs.EntryAttr) *fuseNode {
	clean := cleanFuseRel(rel)
	if clean == "." {
		s.nodeMu.RLock()
		defer s.nodeMu.RUnlock()
		return s.byFuseID[fuseRootID]
	}
	s.nodeMu.Lock()
	defer s.nodeMu.Unlock()
	if attr.Layer == layeredfs.LayerPrivate && attr.NodeID != 0 {
		if fn, ok := s.byCorePrivateID[attr.NodeID]; ok {
			fn.paths[clean] = struct{}{}
			fn.unlinked = false
			s.byRel[clean] = fn
			return fn
		}
		if existing, ok := s.byRel[clean]; ok && existing.corePrivID == 0 {
			existing.corePrivID = attr.NodeID
			existing.paths[clean] = struct{}{}
			existing.unlinked = false
			s.byCorePrivateID[attr.NodeID] = existing
			return existing
		}
		id := s.nextFuseID
		s.nextFuseID++
		fn := &fuseNode{
			fuseID:     id,
			generation: 1,
			corePrivID: attr.NodeID,
			paths:      map[string]struct{}{clean: {}},
		}
		s.byFuseID[id] = fn
		s.byCorePrivateID[attr.NodeID] = fn
		s.byRel[clean] = fn
		return fn
	}
	if fn, ok := s.byRel[clean]; ok {
		fn.unlinked = false
		return fn
	}
	id := s.nextFuseID
	s.nextFuseID++
	fn := &fuseNode{
		fuseID:     id,
		generation: 1,
		paths:      map[string]struct{}{clean: {}},
	}
	s.byFuseID[id] = fn
	s.byRel[clean] = fn
	return fn
}

func (s *Server) recordUnlink(rel string, attr layeredfs.EntryAttr, hadPriorAttr bool) {
	clean := cleanFuseRel(rel)
	s.nodeMu.Lock()
	defer s.nodeMu.Unlock()
	s.dirtyRels[clean] = struct{}{}
	fn, ok := s.byRel[clean]
	if !ok && hadPriorAttr && attr.Layer == layeredfs.LayerPrivate && attr.NodeID != 0 {
		fn = s.byCorePrivateID[attr.NodeID]
	}
	delete(s.byRel, clean)
	if fn != nil {
		delete(fn.paths, clean)
		if len(fn.paths) == 0 {
			fn.unlinked = true
		}
	}
}

func (s *Server) recordRename(oldRel, newRel string) {
	oldClean := cleanFuseRel(oldRel)
	newClean := cleanFuseRel(newRel)
	s.nodeMu.Lock()
	defer s.nodeMu.Unlock()
	s.dirtyRels[oldClean] = struct{}{}
	s.dirtyRels[newClean] = struct{}{}
	prefix := oldClean + "/"
	var moves [][2]string
	for rel := range s.byRel {
		if rel == oldClean {
			moves = append(moves, [2]string{oldClean, newClean})
		} else if strings.HasPrefix(rel, prefix) {
			moves = append(moves, [2]string{rel, newClean + strings.TrimPrefix(rel, oldClean)})
		}
	}
	for _, mv := range moves {
		fn := s.byRel[mv[0]]
		delete(s.byRel, mv[0])
		if fn != nil {
			delete(fn.paths, mv[0])
			fn.paths[mv[1]] = struct{}{}
			s.byRel[mv[1]] = fn
		}
	}
	for fh, h := range s.openHandles {
		if h.rel == oldClean {
			h.rel = newClean
			s.openHandles[fh] = h
		} else if strings.HasPrefix(h.rel, prefix) {
			h.rel = newClean + strings.TrimPrefix(h.rel, oldClean)
			s.openHandles[fh] = h
		}
	}
}
