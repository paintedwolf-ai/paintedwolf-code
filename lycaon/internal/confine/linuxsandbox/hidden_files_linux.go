//go:build linux

package linuxsandbox

import (
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// hiddenFileMask retains one sealed empty inode. Once attached, that read-only
// file mount supplies subsequent clones without another writable source.
type hiddenFileMask struct {
	fd       int
	attached bool
}

func newHiddenFileMask() (mask *hiddenFileMask, resultErr error) {
	fd, err := newTmpfs("0700", maskTmpfsSize)
	if err != nil {
		return nil, err
	}
	anchor, err := os.MkdirTemp("/tmp", ".pw-confine-masks-")
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	target, err := unix.Open(anchor, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == nil {
		err = unix.MoveMount(fd, "", target, "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH)
		_ = unix.Close(target)
	}
	if err != nil {
		_ = unix.Close(fd)
		_ = os.Remove(anchor)
		return nil, err
	}
	// Older kernels require the clone source in this mount namespace. Remove it
	// before any plan masks/read-only mounts and before the target can execute.
	defer func() {
		unmount := unix.Unmount("/proc/self/fd/"+strconv.Itoa(fd), unix.MNT_DETACH)
		closed := unix.Close(fd)
		removed := os.Remove(anchor)
		resultErr = errors.Join(resultErr, unmount, closed, removed)
		if resultErr != nil && mask != nil {
			_ = unix.Close(mask.fd)
			mask = nil
		}
	}()
	file, err := unix.Openat(fd, "hidden", unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.Close(file); err != nil {
		return nil, err
	}
	clone, err := unix.OpenTree(fd, "hidden", unix.OPEN_TREE_CLONE|unix.O_CLOEXEC)
	if err != nil {
		return nil, err
	}
	mask = &hiddenFileMask{fd: clone}
	resultErr = unix.MountSetattr(clone, "", unix.AT_EMPTY_PATH, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY})
	return
}

func (m *hiddenFileMask) hide(path string) error {
	if !m.attached {
		err := unix.MoveMount(m.fd, "", unix.AT_FDCWD, path, unix.MOVE_MOUNT_F_EMPTY_PATH)
		m.attached = err == nil
		return err
	}
	clone, err := unix.OpenTree(m.fd, "", unix.OPEN_TREE_CLONE|unix.AT_EMPTY_PATH|unix.O_CLOEXEC)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(clone) }()
	return unix.MoveMount(clone, "", unix.AT_FDCWD, path, unix.MOVE_MOUNT_F_EMPTY_PATH)
}

func (m *hiddenFileMask) close() error { return unix.Close(m.fd) }
