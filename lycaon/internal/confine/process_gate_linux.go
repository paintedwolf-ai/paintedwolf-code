//go:build linux

package confine

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
	"golang.org/x/sys/unix"
)

const nativeGateVersion = 1
const nativeGateReadyBytes = 10

type nativeGateReady struct {
	PID          int
	ThreadID     int
	ExecutableFD int
	Process      *os.File
	StartTime    int64
	Namespaces   [3]string
	Credentials  unix.Ucred
}

func (r *nativeGateReady) Close() {
	if r.Process != nil {
		_ = r.Process.Close()
		r.Process = nil
	}
}

func nativeGatePair() (*os.File, *os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	if err = unix.SetsockoptInt(fds[0], unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "pinned-launch-host"), os.NewFile(uintptr(fds[1]), "pinned-launch-target"), nil
}

// Send the self-opened pidfd in the same bounded message as the kernel sender
// credentials. An ordinary PID followed by pidfd_open would leave a reuse gap.
func sendNativeGateReady(controlFD, executableFD int) error {
	pidfd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return err
	}
	defer unix.Close(pidfd)
	var packet [nativeGateReadyBytes]byte
	packet[0] = nativeGateVersion
	packet[1] = 1
	binary.LittleEndian.PutUint32(packet[2:6], uint32(executableFD))
	binary.LittleEndian.PutUint32(packet[6:10], uint32(unix.Gettid()))
	return unix.Sendmsg(controlFD, packet[:], unix.UnixRights(pidfd), nil, unix.MSG_NOSIGNAL)
}

func receiveNativeGateReady(ctx context.Context, fd int) (ready nativeGateReady, err error) {
	var packet [nativeGateReadyBytes]byte
	ancillary := make([]byte, unix.CmsgSpace(unix.SizeofUcred)+unix.CmsgSpace(4))
	var n, oob, flags int
	for {
		if err = ctx.Err(); err != nil {
			return ready, err
		}
		n, oob, flags, _, err = unix.Recvmsg(fd, packet[:], ancillary, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC)
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			break
		}
		select {
		case <-ctx.Done():
			return ready, ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
	if err != nil {
		return ready, err
	}
	messages, err := unix.ParseSocketControlMessage(ancillary[:oob])
	if err != nil {
		return ready, err
	}
	var rights []int
	var credentials *unix.Ucred
	defer func() {
		if err != nil {
			for _, right := range rights {
				_ = unix.Close(right)
			}
		}
	}()
	var protocolError error
	for _, message := range messages {
		switch {
		case message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SCM_RIGHTS:
			fds, parseErr := unix.ParseUnixRights(&message)
			if parseErr != nil {
				protocolError = parseErr
				continue
			}
			rights = append(rights, fds...)
		case message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SCM_CREDENTIALS:
			if credentials != nil {
				protocolError = osprocess.ErrExecutableObservationUnavailable
				continue
			}
			credentials, err = unix.ParseUnixCredentials(&message)
			if err != nil {
				protocolError = err
			}
		default:
			protocolError = osprocess.ErrExecutableObservationUnavailable
		}
	}
	if protocolError != nil {
		return ready, protocolError
	}
	if n == 6 && packet[0] == nativeGateVersion && packet[1] == 2 && flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) == 0 && len(rights) == 0 {
		return ready, fmt.Errorf("pinned helper before release: %w", syscall.Errno(binary.LittleEndian.Uint32(packet[2:6])))
	}
	if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || n != len(packet) || packet[0] != nativeGateVersion || packet[1] != 1 || credentials == nil || len(rights) != 1 || credentials.Pid <= 0 {
		return ready, osprocess.ErrExecutableObservationUnavailable
	}
	ready.PID = int(credentials.Pid)
	ready.ThreadID = int(binary.LittleEndian.Uint32(packet[6:10]))
	ready.ExecutableFD = int(binary.LittleEndian.Uint32(packet[2:6]))
	ready.Credentials = *credentials
	if err = verifyGatePidfd(rights[0], ready.PID); err != nil {
		return ready, err
	}
	start, ok := osprocess.StartTime(ready.PID)
	if !ok {
		return ready, osprocess.ErrExecutableObservationUnavailable
	}
	ready.StartTime = start
	for i, namespace := range []string{"user", "pid", "mnt"} {
		ready.Namespaces[i], err = os.Readlink(filepath.Join("/proc", strconv.Itoa(ready.PID), "ns", namespace))
		if err != nil {
			return ready, fmt.Errorf("%w: %w", osprocess.ErrExecutableObservationUnavailable, err)
		}
	}
	if err = verifyGatePidfd(rights[0], ready.PID); err != nil {
		return ready, err
	}
	if after, ok := osprocess.StartTime(ready.PID); !ok || after != start {
		return ready, osprocess.ErrExecutableObservationUnavailable
	}
	ready.Process = os.NewFile(uintptr(rights[0]), "pinned-target-instance")
	return ready, nil
}

// fdinfo names this received pidfd's target in the observer's PID namespace.
// A dead pidfd reports no live target; it never silently follows PID reuse.
func verifyGatePidfd(fd, pid int) error {
	file, err := os.Open(filepath.Join("/proc/self/fdinfo", strconv.Itoa(fd)))
	if err != nil {
		return err
	}
	defer file.Close()
	var buffer [4096]byte
	n, err := file.Read(buffer[:])
	if err != nil {
		return err
	}
	if n == len(buffer) {
		return osprocess.ErrExecutableObservationUnavailable
	}
	found := false
	for _, line := range strings.Split(string(buffer[:n]), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || name != "Pid" {
			continue
		}
		target, parseErr := strconv.Atoi(strings.TrimSpace(value))
		if parseErr != nil || target != pid || found {
			return osprocess.ErrExecutableObservationUnavailable
		}
		found = true
	}
	if !found {
		return osprocess.ErrExecutableObservationUnavailable
	}
	return nil
}
