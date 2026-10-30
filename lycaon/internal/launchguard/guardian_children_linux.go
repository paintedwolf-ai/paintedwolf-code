//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const guardianChildLimit = 32768

// The retained children file belongs to this exact process leader across
// same-PID exec. Only the protected lineage is parented below its subreaper.
func guardianDirectChildren(children *os.File) ([]int, error) {
	raw, err := io.ReadAll(io.NewSectionReader(children, 0, guardianChildLimit*12+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > guardianChildLimit*12 {
		return nil, errors.New("protected child census exceeds bound")
	}
	fields := strings.Fields(string(raw))
	if len(fields) > guardianChildLimit {
		return nil, errors.New("protected child count exceeds bound")
	}
	pids := make([]int, 0, len(fields))
	seen := make(map[int]bool, len(fields))
	for _, field := range fields {
		pid, err := strconv.ParseUint(field, 10, 31)
		if err != nil || pid <= 1 || seen[int(pid)] {
			return nil, errors.New("invalid protected child census")
		}
		seen[int(pid)] = true
		pids = append(pids, int(pid))
	}
	return pids, nil
}

// Hold a proc directory before pidfd_open, then read PPid/UID from that same
// directory afterwards. If the old PID disappears and is reused in between,
// the old proc directory cannot supply the new task's status. Signals use the
// pinned pidfd only after the post-open direct-child check succeeds.
func terminateOwnedLaunchChild(proc *os.File, pid int, allowedUIDs []uint32) error {
	directory, err := unix.Openat(int(proc.Fd()), strconv.Itoa(pid), unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(directory) }()
	pidfd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(pidfd) }()
	fd, err := unix.Openat(directory, "status", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	status := os.NewFile(uintptr(fd), "protected-child-status")
	// UID and PPid precede supplementary groups in the kernel status record.
	raw, readErr := io.ReadAll(io.LimitReader(status, 8192))
	_ = status.Close()
	if readErr != nil {
		return readErr
	}
	parentFound, userFound := false, false
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key {
		case "PPid":
			fields := strings.Fields(value)
			if parentFound || len(fields) != 1 {
				return errors.New("invalid protected child parent record")
			}
			parent, err := strconv.ParseUint(fields[0], 10, 31)
			if err != nil || int(parent) != os.Getpid() {
				return errors.New("protected census task is no longer a direct child")
			}
			parentFound = true
		case "Uid":
			fields := strings.Fields(value)
			if userFound || len(fields) != 4 {
				return errors.New("invalid protected child UID record")
			}
			var selected uint32
			for index, field := range fields {
				uid, err := strconv.ParseUint(field, 10, 32)
				if err != nil {
					return err
				}
				if index == 0 {
					selected = uint32(uid)
				} else if uid != uint64(selected) {
					return errors.New("owned setup child has mixed kernel identities")
				}
			}
			allowed := false
			for _, uid := range allowedUIDs {
				allowed = allowed || selected == uid
			}
			if !allowed {
				return errors.New("owned child has an unexpected kernel UID")
			}
			userFound = true
		}
	}
	if !parentFound || !userFound {
		return errors.New("protected child identity record incomplete")
	}
	if err := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("terminate protected child: %w", err)
	}
	return nil
}
