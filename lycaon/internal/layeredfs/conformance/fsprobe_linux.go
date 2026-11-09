//go:build linux

package conformance

import (
	"fmt"
	"syscall"
)

// linuxFSTypeNames maps statfs(2) f_type magic numbers to filesystem names for
// the filesystems relevant to qualification hosts. Unknown magics are reported
// as fstype_0x… rather than guessed (#1667; test host /mnt/data1 is ZFS).
var linuxFSTypeNames = map[int64]string{
	0xef53:     "ext4",
	0x58465342: "xfs",
	0x9123683e: "btrfs",
	0x01021994: "tmpfs",
	0x2fc12fc1: "zfs",
	0xf2f52010: "f2fs",
	0x6969:     "nfs",
	0x6e667336: "nfs4",
}

// statfsFilesystemType probes the real backing filesystem type via statfs(2).
// Linux reports a numeric magic; the bool is false when the probe fails, and
// callers must report "unknown" rather than guessing (#1667).
func statfsFilesystemType(path string) (string, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", false
	}
	if name, ok := linuxFSTypeNames[st.Type]; ok {
		return name, true
	}
	return fmt.Sprintf("fstype_0x%x", st.Type), true
}
