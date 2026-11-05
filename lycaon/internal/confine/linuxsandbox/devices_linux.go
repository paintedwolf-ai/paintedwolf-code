//go:build linux

package linuxsandbox

import (
	"os"

	"golang.org/x/sys/unix"
)

// devNodes are the host device nodes a confined process may use.
var devNodes = []string{"null", "zero", "full", "random", "urandom", "tty"}

// mountDev replaces /dev with a curated set: the standard character devices,
// pseudo-terminals, a fresh shared-memory tmpfs, and the descriptor links. Raw
// disks and other devices are absent rather than denied.
//
// A command gets its own devpts instance, because the kernel refuses a ptmx
// bound alone: it finds the instance through the pts directory beside the node,
// which only a whole devtmpfs mount would provide. A command whose standard
// streams are a host terminal keeps the host's instance instead, so that
// terminal keeps its name; it cannot allocate another.
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
	ptmxLink := ""
	if !hostTerminalOnStandardStreams() {
		if fd, err := newDevpts(); err == nil {
			clones["pts"] = fd
			ptmxLink = "pts/ptmx"
		}
	}
	if ptmxLink == "" {
		if fd, err := cloneTree("/dev/pts"); err == nil {
			clones["pts"] = fd
		}
		ptmxLink, _ = os.Readlink("/dev/ptmx")
		if ptmxLink == "" {
			if fd, err := unix.OpenTree(unix.AT_FDCWD, "/dev/ptmx", unix.OPEN_TREE_CLONE|unix.O_CLOEXEC); err == nil {
				clones["ptmx"] = fd
			}
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

// unixPtySlaveMajors are the device majors of pseudo-terminal slaves.
const (
	unixPtySlaveMajorFirst = 136
	unixPtySlaveMajorLast  = 143
)

// hostTerminalOnStandardStreams reports a pseudo-terminal slave on a standard
// stream, which can only have come from outside this mount namespace.
func hostTerminalOnStandardStreams() bool {
	for fd := range 3 {
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFCHR {
			continue
		}
		if major := unix.Major(uint64(st.Rdev)); major >= unixPtySlaveMajorFirst && major <= unixPtySlaveMajorLast {
			return true
		}
	}
	return false
}

// newDevpts creates a devpts instance only this sandbox can reach.
func newDevpts() (int, error) {
	fs, err := unix.Fsopen("devpts", unix.FSOPEN_CLOEXEC)
	if err != nil {
		return -1, err
	}
	defer func() { _ = unix.Close(fs) }()
	for key, value := range map[string]string{"ptmxmode": "0666", "mode": "0600"} {
		if err := unix.FsconfigSetString(fs, key, value); err != nil {
			return -1, err
		}
	}
	if err := unix.FsconfigCreate(fs); err != nil {
		return -1, err
	}
	return unix.Fsmount(fs, unix.FSMOUNT_CLOEXEC, unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NOEXEC)
}
