//go:build linux

package launchguard

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// fdinfo reports cached VFS descriptor facts without a filesystem getattr.
// The privileged stage must not issue getattr against a caller's FUSE daemon.
func mountDescriptorIdentity(fd int) (id, inode uint64, err error) {
	if fd < 0 {
		return 0, 0, errors.New("invalid held source descriptor")
	}
	// #nosec G703 -- only a nonnegative decimal descriptor selects fixed proc fdinfo; no caller path or filesystem getattr is used.
	file, err := os.Open("/proc/thread-self/fdinfo/" + strconv.Itoa(fd))
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(raw) > 8192 {
		return 0, 0, errors.New("invalid source descriptor kernel facts")
	}
	var seenID, seenInode bool
	for line := range strings.SplitSeq(string(raw), "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found || (key != "mnt_id" && key != "ino") {
			continue
		}
		number, parseErr := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if parseErr != nil || number == 0 {
			return 0, 0, errors.New("invalid source descriptor identity")
		}
		if key == "mnt_id" {
			if seenID {
				return 0, 0, errors.New("duplicate descriptor mount identity")
			}
			id, seenID = number, true
		} else {
			if seenInode {
				return 0, 0, errors.New("duplicate descriptor inode identity")
			}
			inode, seenInode = number, true
		}
	}
	if !seenID || !seenInode {
		return 0, 0, errors.New("incomplete source descriptor identity")
	}
	return id, inode, nil
}

func verifyMountSourceRoot(fd int, expected mountEntry) error {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_PATH == 0 {
		return errors.New("mount source is not an opened path descriptor")
	}
	id, _, err := mountDescriptorIdentity(fd)
	if err != nil || id != uint64(expected.id) {
		return errors.New("mount source descriptor identity differs from selection")
	}
	alias, err := os.Readlink(mountDescriptorPath(fd))
	if err != nil || alias != expected.mountPoint {
		return errors.New("mount source descriptor is not the selected root alias")
	}
	mountinfo, err := os.Open("/proc/thread-self/mountinfo")
	if err != nil {
		return err
	}
	defer func() { _ = mountinfo.Close() }()
	raw, err := readMountInfo(int(mountinfo.Fd()), nil)
	if err != nil {
		return err
	}
	entries, err := parseMountTopology(raw)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.id == expected.id && entry == expected {
			return nil
		}
	}
	return errors.New("mount source topology changed before descriptor admission")
}
