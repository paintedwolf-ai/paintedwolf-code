//go:build unix

package launchverify

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// sharedFlockHeld reports whether lock currently holds a shared flock. When the
// verifier can open the lock descriptor, it probes the kernel lock directly;
// when the lock is root-only (0600) and the verifier runs as an unprivileged
// service user, it reads the kernel's flock table in procfs for the verified
// lock device and inode.
func sharedFlockHeld(file *os.File, procRoot string, launcherPID int, lock fileFacts) bool {
	if file != nil {
		info, err := file.Stat()
		if err != nil {
			return false
		}
		opened, err := factsOf(info)
		if err != nil || !sameInode(opened, lock) {
			return false
		}
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			return false
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return false
		}
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
			return false
		}
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		return true
	}
	return procHasSharedFlock(procRoot, launcherPID, lock)
}

func procHasSharedFlock(procRoot string, launcherPID int, lock fileFacts) bool {
	if launcherPID > 0 {
		fdinfoDir := filepath.Join(procRoot, strconv.Itoa(launcherPID), "fdinfo")
		if entries, err := os.ReadDir(fdinfoDir); err == nil {
			for _, entry := range entries {
				raw, err := readBounded(filepath.Join(fdinfoDir, entry.Name()), 64<<10)
				if err == nil && hasSharedFlockLine(string(raw), lock) {
					return true
				}
			}
		}
	}
	raw, err := readBounded(filepath.Join(procRoot, "locks"), maxMapsBytes)
	if err != nil {
		return false
	}
	return hasSharedFlockLine(string(raw), lock)
}

func hasSharedFlockLine(content string, lock fileFacts) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "lock:"))
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[1] != "FLOCK" || fields[2] != "ADVISORY" || fields[3] != "READ" {
			continue
		}
		parts := strings.Split(fields[5], ":")
		if len(parts) != 3 {
			continue
		}
		dev, err := mapsDevice(parts[0] + ":" + parts[1])
		if err != nil || dev != lock.device {
			continue
		}
		ino, err := strconv.ParseUint(parts[2], 10, 64)
		if err == nil && ino == lock.inode {
			return true
		}
	}
	return false
}
