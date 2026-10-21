//go:build linux

package linuxsandbox

import (
	"os"

	"golang.org/x/sys/unix"
)

// devNodes are the host device nodes a confined process may use.
var devNodes = []string{"null", "zero", "full", "random", "urandom", "tty"}

// mountDev replaces /dev with a curated set: the standard character devices,
// the host's pseudo-terminals so inherited terminals keep their names, a fresh
// shared-memory tmpfs, and the descriptor links. Raw disks and other devices
// are absent rather than denied.
func mountDev() error {
	clones := map[string]int{}
	defer func() {
		for _, fd := range clones {
			_ = unix.Close(fd)
		}
	}()
	for _, name := range devNodes {
		fd, err := unix.OpenTree(unix.AT_FDCWD, "/dev/"+name, unix.OPEN_TREE_CLONE|unix.O_CLOEXEC)
		if err != nil {
			return mountFailure("/dev/"+name, err)
		}
		clones[name] = fd
	}
	if fd, err := cloneTree("/dev/pts"); err == nil {
		clones["pts"] = fd
	}
	ptmxLink, _ := os.Readlink("/dev/ptmx")
	if ptmxLink == "" {
		if fd, err := unix.OpenTree(unix.AT_FDCWD, "/dev/ptmx", unix.OPEN_TREE_CLONE|unix.O_CLOEXEC); err == nil {
			clones["ptmx"] = fd
		}
	}
	dev, err := newTmpfs("0755", maskTmpfsSize)
	if err != nil {
		return mountFailure("/dev", err)
	}
	defer func() { _ = unix.Close(dev) }()
	if err := unix.MoveMount(dev, "", unix.AT_FDCWD, "/dev", unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return mountFailure("/dev", err)
	}
	for name, fd := range clones {
		target := "/dev/" + name
		if name == "pts" {
			if err := os.Mkdir(target, 0o755); err != nil {
				return mountFailure(target, err)
			}
		} else {
			if err := createPlaceholder(target, 0o666); err != nil {
				return mountFailure(target, err)
			}
		}
		if err := unix.MoveMount(fd, "", unix.AT_FDCWD, target, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
			return mountFailure(target, err)
		}
	}
	if ptmxLink != "" {
		if err := os.Symlink(ptmxLink, "/dev/ptmx"); err != nil {
			return mountFailure("/dev/ptmx", err)
		}
	}
	for link, target := range map[string]string{"fd": "/proc/self/fd", "stdin": "/proc/self/fd/0", "stdout": "/proc/self/fd/1", "stderr": "/proc/self/fd/2"} {
		if err := os.Symlink(target, "/dev/"+link); err != nil {
			return mountFailure("/dev/"+link, err)
		}
	}
	if err := os.Mkdir("/dev/shm", 0o1777); err != nil {
		return mountFailure("/dev/shm", err)
	}
	shm, err := newTmpfs("1777", sharedMemorySize)
	if err != nil {
		return mountFailure("/dev/shm", err)
	}
	defer func() { _ = unix.Close(shm) }()
	if err := unix.MoveMount(shm, "", unix.AT_FDCWD, "/dev/shm", unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return mountFailure("/dev/shm", err)
	}
	if err := setReadOnlyAt("/dev"); err != nil {
		return mountFailure("/dev", err)
	}
	return nil
}
