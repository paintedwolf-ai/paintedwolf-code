//go:build linux

package linuxsandbox

import (
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// hiddenFileMount temporarily attaches the empty mask source inside the private
// mount namespace. Older kernels cannot clone files from a detached mount.
type hiddenFileMount struct {
	fd     int
	anchor string
}

func newHiddenFileMount() (*hiddenFileMount, error) {
	fd, err := newTmpfs("0700")
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
	mask := &hiddenFileMount{fd: fd, anchor: anchor}
	file, err := unix.Openat(fd, "hidden", unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC, 0)
	if err == nil {
		err = unix.Close(file)
	}
	if err != nil {
		_ = mask.close()
		return nil, err
	}
	return mask, nil
}

func (m *hiddenFileMount) hide(path string) error {
	clone, err := unix.OpenTree(m.fd, "hidden", unix.OPEN_TREE_CLONE|unix.O_CLOEXEC)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(clone) }()
	if err := unix.MountSetattr(clone, "", unix.AT_EMPTY_PATH, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY}); err != nil {
		return err
	}
	return unix.MoveMount(clone, "", unix.AT_FDCWD, path, unix.MOVE_MOUNT_F_EMPTY_PATH)
}

// close removes the writable source before the target runs. The mount FD pins
// the actual attachment even if another process renames the empty anchor.
func (m *hiddenFileMount) close() error {
	unmount := unix.Unmount("/proc/self/fd/"+strconv.Itoa(m.fd), unix.MNT_DETACH)
	closed := unix.Close(m.fd)
	removed := os.Remove(m.anchor)
	return errors.Join(unmount, closed, removed)
}
