//go:build darwin || linux

package socketref

import (
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

const endpointMessageBytes = 16 << 10
const endpointMessageFDs = 253

// readUnixMessage sets close-on-exec before another Go launch can inherit any
// received descriptor. The fork lock covers only the nonblocking syscall.
func readUnixMessage(conn *net.UnixConn, data, control []byte) (n, oobn int, fds []int, err error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, nil, err
	}
	var readErr error
	err = raw.Read(func(fd uintptr) bool {
		syscall.ForkLock.RLock()
		defer syscall.ForkLock.RUnlock()
		var flags int
		n, oobn, flags, _, readErr = unix.Recvmsg(int(fd), data, control, 0)
		if errors.Is(readErr, unix.EAGAIN) || errors.Is(readErr, unix.EINTR) {
			return false
		}
		if readErr != nil {
			return true
		}
		messages, parseErr := unix.ParseSocketControlMessage(control[:oobn])
		readErr = parseErr
		for _, message := range messages {
			if message.Header.Level != unix.SOL_SOCKET || message.Header.Type != unix.SCM_RIGHTS {
				readErr = fmt.Errorf("local service sent unsupported ancillary data")
				continue
			}
			rights, rightsErr := unix.ParseUnixRights(&message)
			if rightsErr != nil {
				readErr = errors.Join(readErr, rightsErr)
				continue
			}
			for _, received := range rights {
				unix.CloseOnExec(received)
			}
			fds = append(fds, rights...)
		}
		if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
			readErr = errors.Join(readErr, fmt.Errorf("local service message exceeds its forwarding bound"))
		}
		return true
	})
	return n, oobn, fds, errors.Join(err, readErr)
}

func forwardUnixMessages(dst, src *net.UnixConn) error {
	data := make([]byte, endpointMessageBytes)
	control := make([]byte, unix.CmsgSpace(4*endpointMessageFDs))
	for {
		n, oobn, fds, err := readUnixMessage(src, data, control)
		if err == nil && n > 0 {
			err = writeUnixMessage(dst, data[:n], control[:oobn])
		}
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return dst.CloseWrite()
		}
	}
}

func writeUnixMessage(dst *net.UnixConn, data, control []byte) error {
	n, oobn, err := dst.WriteMsgUnix(data, control, nil)
	if err != nil {
		return err
	}
	if oobn != len(control) || n == 0 {
		return io.ErrShortWrite
	}
	for n < len(data) {
		written, err := dst.Write(data[n:])
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		n += written
	}
	return nil
}
