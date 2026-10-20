//go:build linux

package confine

import (
	"encoding/binary"
	"errors"
	"os"
	"runtime"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

func execPinnedTarget(_ string, argv []string, cwdFD, controlFD int) (err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer func() {
		if err != nil {
			var packet [6]byte
			packet[0], packet[1] = nativeGateVersion, 2
			var errno syscall.Errno
			if !errors.As(err, &errno) {
				errno = syscall.EIO
			}
			binary.LittleEndian.PutUint32(packet[2:], uint32(errno))
			_ = unix.Sendmsg(controlFD, packet[:], nil, nil, unix.MSG_NOSIGNAL)
		}
	}()
	imageFD := helperPinnedExecutableFDArg
	if imageFD < 0 || len(argv) == 0 {
		return syscall.EINVAL
	}
	var header [4]byte
	n, err := unix.Pread(imageFD, header[:], 0)
	if err != nil {
		return err
	}
	if n != 4 || header != [4]byte{0x7f, 'E', 'L', 'F'} {
		return syscall.ENOEXEC
	}
	if err := unix.Fchdir(cwdFD); err != nil {
		return err
	}
	_ = unix.Close(cwdFD)
	helperPinnedCwdFDArg = -1
	unix.CloseOnExec(imageFD)
	unix.CloseOnExec(controlFD)
	if _, err := heldExecutablePath(imageFD); err != nil {
		return err
	}
	if err := sendNativeGateReady(controlFD, imageFD); err != nil {
		return err
	}
	var release [2]byte
	n, _, flags, _, err := unix.Recvmsg(controlFD, release[:], nil, 0)
	if err != nil {
		return err
	}
	if n != len(release) || flags&unix.MSG_TRUNC != 0 || release != [2]byte{nativeGateVersion, 3} {
		return syscall.EPROTO
	}
	return nativeHeldExec(imageFD, argv, os.Environ())
}

// Use the existing Go Exec door: it owns runtime thread exclusion and restoration
// of the inherited native descriptor limit. There is no path-exec fallback.
func nativeHeldExec(fd int, argv, environment []string) error {
	if len(argv) == 0 {
		return syscall.EINVAL
	}
	path, err := heldExecutablePath(fd)
	if err != nil {
		return err
	}
	return unix.Exec(path, argv, environment)
}

func heldExecutablePath(fd int) (string, error) {
	path := "/proc/self/fd/" + strconv.Itoa(fd)
	var filesystem unix.Statfs_t
	if err := unix.Statfs("/proc/self/fd", &filesystem); err != nil {
		return "", err
	}
	if filesystem.Type != unix.PROC_SUPER_MAGIC {
		return "", syscall.ENOTSUP
	}
	var held, selected unix.Stat_t
	if err := unix.Fstat(fd, &held); err != nil {
		return "", err
	}
	if err := unix.Stat(path, &selected); err != nil {
		return "", err
	}
	if held.Dev != selected.Dev || held.Ino != selected.Ino {
		return "", ErrLaunchIdentityChanged
	}
	return path, nil
}
