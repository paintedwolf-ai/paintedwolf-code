//go:build linux

package confine_test

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const metadataScriptEnv = "PW_CONFINE_METADATA_SCRIPT"

func TestLinuxMetadataProbeProcess(t *testing.T) {
	script, ok := os.LookupEnv(metadataScriptEnv)
	if !ok {
		return
	}
	scanner := bufio.NewScanner(strings.NewReader(script))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			os.Exit(21)
		}
		fmt.Printf("%s: %s\n", scanner.Text(), floorsResult(metadataOperation(fields[0], fields[1])))
	}
	if scanner.Err() != nil {
		os.Exit(22)
	}
	os.Exit(0)
}

func metadataOperation(op, path string) error {
	times := []unix.Timespec{{Sec: 123456789}, {Sec: 123456789}}
	switch op {
	case "restricted-mount":
		var st unix.Statfs_t
		if err := unix.Statfs(path, &st); err != nil {
			return err
		}
		want := int64(unix.ST_RDONLY | unix.ST_NOEXEC | unix.ST_NOSUID | unix.ST_NODEV)
		if st.Flags&want != want {
			return fmt.Errorf("source mount restrictions missing: %x", st.Flags)
		}
		return nil
	case "chmod":
		return unix.Chmod(path, 0o640)
	case "fchmodat":
		return unix.Fchmodat(unix.AT_FDCWD, path, 0o640, 0)
	case "chown":
		return unix.Chown(path, -1, os.Getgid())
	case "lchown":
		return unix.Lchown(path, -1, os.Getgid())
	case "fchownat":
		return unix.Fchownat(unix.AT_FDCWD, path, -1, os.Getgid(), 0)
	case "utimes":
		return unix.UtimesNano(path, times)
	case "utimensat":
		return unix.UtimesNanoAt(unix.AT_FDCWD, path, times, 0)
	case "lutimensat":
		return unix.UtimesNanoAt(unix.AT_FDCWD, path, times, unix.AT_SYMLINK_NOFOLLOW)
	case "setxattr":
		return unix.Setxattr(path, "user.pw-metadata", []byte("after"), 0)
	case "lsetxattr":
		return unix.Lsetxattr(path, "user.pw-metadata", []byte("after"), 0)
	case "removexattr":
		return unix.Removexattr(path, "user.pw-remove")
	case "lremovexattr":
		return unix.Lremovexattr(path, "user.pw-remove-link")
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC
	if strings.HasSuffix(op, "-empty") {
		flags = unix.O_PATH | unix.O_CLOEXEC
	}
	var fd int
	var err error
	switch op {
	case "openat2-fchmod":
		fd, err = unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: uint64(flags)})
	case "open-create-fchmod":
		fd, err = unix.Open(path, flags|unix.O_CREAT, 0o644)
	default:
		fd, err = unix.Open(path, flags, 0)
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	switch op {
	case "fchmod", "openat2-fchmod", "open-create-fchmod":
		return unix.Fchmod(fd, 0o640)
	case "fchmodat-empty":
		return unix.Fchmodat(fd, "", 0o640, unix.AT_EMPTY_PATH)
	case "fchown":
		return unix.Fchown(fd, -1, os.Getgid())
	case "fchownat-empty":
		return unix.Fchownat(fd, "", -1, os.Getgid(), unix.AT_EMPTY_PATH)
	case "futimes":
		return unix.Futimes(fd, []unix.Timeval{{Sec: 123456789}, {Sec: 123456789}})
	case "utimensat-empty":
		return unix.UtimesNanoAt(fd, "", times, unix.AT_EMPTY_PATH)
	case "fsetxattr":
		return unix.Fsetxattr(fd, "user.pw-metadata", []byte("after"), 0)
	case "fremovexattr":
		return unix.Fremovexattr(fd, "user.pw-remove-fd")
	case "setflags":
		before, err := unix.IoctlGetInt(fd, unix.FS_IOC_GETFLAGS)
		if err != nil {
			return err
		}
		return unix.IoctlSetPointerInt(fd, unix.FS_IOC_SETFLAGS, before^0x40) // FS_NODUMP_FL is owner-settable.
	}
	return fmt.Errorf("unknown metadata operation %s", op)
}
