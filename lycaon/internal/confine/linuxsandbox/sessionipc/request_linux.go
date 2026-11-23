package sessionipc

import (
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

const (
	register byte = 1
	lookup   byte = 2
	reserve  byte = 3
	discard  byte = 4
)

func socketPair() ([2]int, error) {
	return unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
}

// Reserve acquires capacity before bind creates a filesystem entry.
func Reserve(channel, socket int) unix.Errno { return ask(channel, socket, reserve) }

// Discard releases the reservation for an unsuccessful bind.
func Discard(channel, socket int) { _ = ask(channel, socket, discard) }

// Register records the socket just bound by the restricted performer.
func Register(channel, bound int) unix.Errno { return ask(channel, bound, register) }

// Admits checks the pinned destination against the session's registrations.
func Admits(channel, destination int) bool { return ask(channel, destination, lookup) == 0 }

func ask(channel, fd int, op byte) unix.Errno {
	pair, err := socketPair()
	if err != nil {
		return unix.EIO
	}
	defer func() { _ = unix.Close(pair[0]) }()
	rights := unix.UnixRights(fd, pair[1])
	for {
		err = unix.Sendmsg(channel, []byte{op}, rights, nil, unix.MSG_NOSIGNAL)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	_ = unix.Close(pair[1])
	if err != nil {
		return unix.EIO
	}
	var reply [4]byte
	for {
		n, err := unix.Read(pair[0], reply[:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n != len(reply) {
			return unix.EIO
		}
		return unix.Errno(binary.LittleEndian.Uint32(reply[:]))
	}
}
