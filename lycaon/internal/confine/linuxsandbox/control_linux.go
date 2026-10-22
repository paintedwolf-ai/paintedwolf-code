//go:build linux

package linuxsandbox

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func controlPair() (*os.File, *os.File, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	// Only the outer endpoint uses Go's poller; the child uses blocking syscalls.
	if err := unix.SetNonblock(pair[0], true); err != nil {
		_ = unix.Close(pair[0])
		_ = unix.Close(pair[1])
		return nil, nil, err
	}
	return os.NewFile(uintptr(pair[0]), "control"), os.NewFile(uintptr(pair[1]), "control-child"), nil
}

func receiveControl(control *os.File, header, rights []byte) (n, oobn int, err error) {
	raw, err := control.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	// Read holds the descriptor through Recvmsg and wakes when Close interrupts polling.
	readErr := raw.Read(func(fd uintptr) bool {
		n, oobn, _, _, err = unix.Recvmsg(int(fd), header, rights, unix.MSG_CMSG_CLOEXEC)
		return !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK)
	})
	if readErr != nil {
		return 0, 0, readErr
	}
	return n, oobn, err
}
