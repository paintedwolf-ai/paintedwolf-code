//go:build darwin || linux

package socketref

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const endpointMessageBytes = 16 << 10
const endpointMessageFDs = 253

type descriptorCustody struct {
	sync.Mutex
	retained int
	limit    int
}

var endpointDescriptors = descriptorCustody{limit: 1024}

func (c *descriptorCustody) release(fds []int) {
	c.Lock()
	defer c.Unlock()
	for _, fd := range fds {
		_ = unix.Close(fd)
	}
	c.retained -= len(fds)
}

// Go's WriteMsgUnix refuses connected datagrams and can insert a byte for empty
// ancillary messages. Send the exact datagram through its existing poller.
func writeUnixDatagram(conn *net.UnixConn, data, control []byte, peer *net.UnixAddr) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var message unix.Msghdr
	var vector unix.Iovec
	if len(data) > 0 {
		vector.Base = &data[0]
		vector.SetLen(len(data))
	}
	message.Iov = &vector
	message.SetIovlen(1)
	if len(control) > 0 {
		message.Control = &control[0]
		message.SetControllen(len(control))
	}
	var address unix.RawSockaddrUnix
	if peer != nil {
		if len(peer.Name) >= len(address.Path) {
			return unix.ENAMETOOLONG
		}
		address.Family = unix.AF_UNIX
		for i := 0; i < len(peer.Name); i++ {
			address.Path[i] = int8(peer.Name[i])
		}
		message.Namelen = uint32(unsafe.Offsetof(address.Path)) + uint32(len(peer.Name)) + 1
		setUnixAddressLength(&address, message.Namelen)
		message.Name = (*byte)(unsafe.Pointer(&address))
	}
	var sendErr error
	err = raw.Write(func(fd uintptr) bool {
		n, _, code := unix.Syscall(unix.SYS_SENDMSG, fd, uintptr(unsafe.Pointer(&message)), 0)
		if code == unix.EAGAIN || code == unix.EINTR {
			return false
		}
		if code != 0 {
			sendErr = code
		} else if int(n) != len(data) {
			sendErr = io.ErrShortWrite
		}
		return true
	})
	return errors.Join(err, sendErr)
}

// read serializes descriptor admission and sets close-on-exec before another
// launch can inherit received rights. Neither lock spans a poll wait.
func (c *descriptorCustody) read(conn *net.UnixConn, data, control []byte) (n, oobn int, fds []int, sender *unix.SockaddrUnix, err error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, nil, nil, err
	}
	var readErr error
	err = raw.Read(func(fd uintptr) bool {
		c.Lock()
		defer c.Unlock()
		syscall.ForkLock.RLock()
		defer syscall.ForkLock.RUnlock()
		var flags int
		var address unix.Sockaddr
		n, oobn, flags, address, readErr = unix.Recvmsg(int(fd), data, control, 0)
		sender, _ = address.(*unix.SockaddrUnix)
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
		if len(fds) > c.limit-c.retained {
			for _, received := range fds {
				_ = unix.Close(received)
			}
			fds = nil
			readErr = errors.Join(readErr, fmt.Errorf("local service descriptor custody limit reached"))
		} else {
			c.retained += len(fds)
		}
		return true
	})
	return n, oobn, fds, sender, errors.Join(err, readErr)
}

func forwardUnixMessages(dst, src *net.UnixConn) error {
	data := make([]byte, endpointMessageBytes)
	control := make([]byte, unix.CmsgSpace(4*endpointMessageFDs))
	for {
		n, oobn, fds, _, err := endpointDescriptors.read(src, data, control)
		if err == nil && n > 0 {
			err = writeUnixMessage(dst, data[:n], control[:oobn])
		}
		endpointDescriptors.release(fds)
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
