//go:build linux

package launchguard

import (
	"errors"
	"math"

	"golang.org/x/sys/unix"
)

// A seq-file can span multiple kernel topology epochs. Only a capture with no
// intervening mount event may reach the parser and descriptor admission.
func readIntakeMountTopology(fd int, read func(int, []byte) ([]byte, error)) ([]mountEntry, bool, error) {
	if _, err := mountInfoEpochChanged(fd); err != nil {
		return nil, false, &intakeMonitorFailure{intakeFailurePoll, err}
	}
	raw, err := read(fd, nil)
	if err != nil {
		return nil, false, &intakeMonitorFailure{intakeFailureMountInfo, err}
	}
	changed, err := mountInfoEpochChanged(fd)
	if err != nil {
		return nil, false, &intakeMonitorFailure{intakeFailurePoll, err}
	}
	if changed {
		return nil, true, nil
	}
	entries, err := parseMountTopology(raw)
	if err != nil {
		return nil, false, &intakeMonitorFailure{intakeFailureTopology, err}
	}
	return entries, false, nil
}

func mountInfoEpochChanged(fd int) (bool, error) {
	if fd < 0 || fd > math.MaxInt32 {
		return false, unix.EBADF
	}
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLPRI | unix.POLLERR}}
	for {
		_, err := unix.Poll(fds, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, err
		}
		if fds[0].Revents & ^int16(unix.POLLPRI|unix.POLLERR) != 0 {
			return false, errors.New("intake mountinfo descriptor has unexpected poll state")
		}
		return fds[0].Revents != 0, nil
	}
}
