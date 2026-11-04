//go:build linux

package fuselinux

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"golang.org/x/sys/unix"
)

const (
	fuseHeaderOutSize = 16
	fuseMaxReadBuffer = 1024 * 1024
)

func mountAndStartFUSEServer(ctx context.Context, adapterCfg Config, srvCfg ServerConfig, core *layeredfs.Core) (*Server, error) {
	mountCtx, cancelMount := context.WithTimeout(ctx, adapterCfg.MountTimeout)
	defer cancelMount()
	fuseFD, err := receiveFUSEDevFD(mountCtx, adapterCfg.FusermountBin, srvCfg.MountOptions, srvCfg.MountPath)
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
	srv.start()
	return srv, nil
}

func (s *Server) start() {
	s.wg.Add(1 + s.workerCount)
	go s.serveLoop()
	for range s.workerCount {
		go s.ordinaryWorker()
	}
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

	var stderr bytes.Buffer
	wait, err := execpkg.StartDescriptorHelper(ctx, execpkg.HostLaunch("fuse_mount"), fusermountBin, []string{"-o", opts, "--", mountPath},
		[]string{"_FUSE_COMMFD=3"}, childSockFile, &stderr)
	if err != nil {
		return -1, fmt.Errorf("fuselinux: start %s: %w", fusermountBin, err)
	}
	_ = childSockFile.Close()
	childSockFile = nil

	buf := make([]byte, 64)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, recvErr := unix.Recvmsg(parentSock, buf, oob, unix.MSG_CMSG_CLOEXEC)
	waitErr := wait()
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
		s.markClosed()
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
		if n == 0 {
			return
		}
		if n < fuseHeaderInSize {
			continue
		}
		req := make([]byte, n)
		copy(req, buf[:n])
		s.dispatchRequest(req)
	}
}

func fuseOutFrame(unique uint64, errno int32, payload []byte) []byte {
	total := fuseHeaderOutSize + len(payload)
	out := make([]byte, total)
	binary.LittleEndian.PutUint32(out[0:4], uint32(total))
	binary.LittleEndian.PutUint32(out[4:8], uint32(errno))
	binary.LittleEndian.PutUint64(out[8:16], unique)
	copy(out[16:], payload)
	return out
}

// writeReply needs no lock: each write to /dev/fuse is atomic, and every caller is tracked by wg,
// so the descriptor outlives it. Replies continue after close begins so that blocked callers get
// their errors and any notification waiting on them can finish.
func (s *Server) writeReply(unique uint64, errno int32, payload []byte) error {
	_, err := unix.Write(s.fd, fuseOutFrame(unique, errno, payload))
	return err
}

// writeNotification shares no lock with replies. The kernel can hold it on an inode lock or a page
// that an in-flight request owns, and that request can only finish once its reply is written.
func (s *Server) writeNotification(code int32, payload []byte) {
	s.notifyMu.RLock()
	defer s.notifyMu.RUnlock()
	if !s.closed.Load() {
		_, _ = unix.Write(s.fd, fuseOutFrame(0, code, payload))
	}
}

const (
	fuseNotifyInvalInode = 2
	fuseNotifyInvalEntry = 3
)

func notifyInvalInode(s *Server, ino uint64) {
	if !s.IsAlive() || ino == 0 {
		return
	}
	var payload [24]byte
	binary.LittleEndian.PutUint64(payload[0:8], ino)
	binary.LittleEndian.PutUint64(payload[8:16], 0)
	binary.LittleEndian.PutUint64(payload[16:24], ^uint64(0))
	s.writeNotification(fuseNotifyInvalInode, payload[:])
}

func notifyInvalEntry(s *Server, parent uint64, name string) {
	if !s.IsAlive() || parent == 0 || name == "" {
		return
	}
	payload := make([]byte, 16+len(name)+1)
	binary.LittleEndian.PutUint64(payload[0:8], parent)
	binary.LittleEndian.PutUint32(payload[8:12], uint32(len(name)))
	copy(payload[16:], name)
	s.writeNotification(fuseNotifyInvalEntry, payload)
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
