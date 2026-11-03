//go:build linux

package fuselinux

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"golang.org/x/sys/unix"
)

const (
	fuseOpPoll      = 40
	pollProbeName   = ".painted-wolf-poll-probe"
	pollProbeNodeID = uint64(1) << 62
	pollProbeFH     = uint64(1) << 62
)

// optOutOfFUSEPoll spends the connection's one FUSE_POLL before anyone else can. Go's os.Open
// registers every file with its poller, and the runtime makes that call without releasing its
// scheduler slot. On FUSE the registration needs a server reply until the kernel learns polling
// is unsupported, so a stop-the-world during the first open in this process would hang the engine.
// This open and poll are ordinary system calls, safe at any time. The probe entry exists only here.
func (s *Server) optOutOfFUSEPoll(mountPath string) error {
	s.pollProbeActive.Store(true)
	defer s.pollProbeActive.Store(false)
	fd, err := unix.Open(filepath.Join(mountPath, pollProbeName), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("fuselinux: open poll probe: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	if _, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 0); err != nil {
		return fmt.Errorf("fuselinux: poll probe: %w", err)
	}
	if s.pollRequests.Load() == 0 {
		return fmt.Errorf("fuselinux: poll probe did not reach the server")
	}
	return nil
}

func pollProbeAttr() layeredfs.EntryAttr {
	return layeredfs.EntryAttr{Kind: layeredfs.EntryRegular, Mode: 0o400, Nlink: 1}
}

func pollProbeOpenOut() []byte {
	out := make([]byte, 16)
	binary.LittleEndian.PutUint64(out[0:8], pollProbeFH)
	return out
}

func pollUnsupported() ([]byte, int32) { return nil, -int32(syscall.ENOSYS) }
