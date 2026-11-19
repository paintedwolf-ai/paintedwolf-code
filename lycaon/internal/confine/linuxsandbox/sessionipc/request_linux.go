package sessionipc

import "golang.org/x/sys/unix"

const (
	register byte = 1
	lookup   byte = 2
)

func socketPair() ([2]int, error) {
	return unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
}

// Register records the socket just bound by the restricted performer.
func Register(channel, bound int) bool { return ask(channel, bound, register) }

// Admits checks the pinned destination against the session's registrations.
func Admits(channel, destination int) bool { return ask(channel, destination, lookup) }

func ask(channel, fd int, op byte) bool {
	pair, err := socketPair()
	if err != nil {
		return false
	}
	defer unix.Close(pair[0])
	rights := unix.UnixRights(fd, pair[1])
	err = unix.Sendmsg(channel, []byte{op}, rights, nil, unix.MSG_NOSIGNAL)
	unix.Close(pair[1])
	if err != nil {
		return false
	}
	var reply [1]byte
	for {
		n, err := unix.Read(pair[0], reply[:])
		if err == unix.EINTR {
			continue
		}
		return err == nil && n == 1 && reply[0] == 1
	}
}
