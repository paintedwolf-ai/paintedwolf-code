//go:build linux

package linuxsandbox

import "golang.org/x/sys/unix"

const maskTmpfsSize = "1m"

// Private shared memory is sparse and capped independently for every launch.
const sharedMemorySize = "64m"

// newTmpfs creates an unattached tmpfs with the given root mode.
func newTmpfs(mode, size string) (int, error) {
	fs, err := unix.Fsopen("tmpfs", unix.FSOPEN_CLOEXEC)
	if err != nil {
		return -1, err
	}
	defer func() { _ = unix.Close(fs) }()
	for key, value := range map[string]string{"mode": mode, "size": size} {
		if err := unix.FsconfigSetString(fs, key, value); err != nil {
			return -1, err
		}
	}
	if err := unix.FsconfigCreate(fs); err != nil {
		return -1, err
	}
	return unix.Fsmount(fs, unix.FSMOUNT_CLOEXEC, unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NODEV)
}
