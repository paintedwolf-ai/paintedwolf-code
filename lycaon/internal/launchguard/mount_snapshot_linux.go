//go:build linux

package launchguard

import (
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
	id, _, err := mountDescriptorIdentity(fd)
	return id, err
}
