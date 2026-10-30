//go:build linux

package launchguard

import (
	"errors"

	"golang.org/x/sys/unix"
)

func openMountPoint(name string) (int, error) {
	if err := (mountTransfer{kind: mountTransferAdd, sequence: 1, id: 1, path: name}).validate(); err != nil {
		return -1, unix.EINVAL
	}
	return unix.Openat2(unix.AT_FDCWD, name, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
}

func mountID(fd int) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &stat); err != nil {
		return 0, err
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 || stat.Mnt_id == 0 {
		return 0, errors.New("kernel did not report mount identity")
	}
	return stat.Mnt_id, nil
}
