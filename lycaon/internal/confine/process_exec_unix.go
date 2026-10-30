//go:build darwin || linux

package confine

import (
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// execNativeTarget keeps ordinary native pathname/shebang/credential semantics.
// It never substitutes a descriptor pathname, interpreter argv or traced exec.
func execNativeTarget(path string, argv []string) (err error) {
	if helperPinnedReadyFDArg < 0 {
		return unix.Exec(path, argv, os.Environ())
	}
	readyFD, releaseFD := helperPinnedReadyFDArg, helperPinnedReleaseFDArg
	if releaseFD < 0 || helperPinnedCwdFDArg < 0 || helperPinnedExecutableFDArg < 0 {
		return syscall.EINVAL
	}
	defer func() {
		if err == nil {
			return
		}
		var errno syscall.Errno
		if !errors.As(err, &errno) {
			errno = syscall.EIO
		}
		var record [5]byte
		record[0] = 2
		binary.LittleEndian.PutUint32(record[1:], uint32(errno))
		_, _ = unix.Write(readyFD, record[:])
	}()
	if err := unix.Fchdir(helperPinnedCwdFDArg); err != nil {
		return err
	}
	_ = unix.Close(helperPinnedCwdFDArg)
	helperPinnedCwdFDArg = -1
	path = helperPinnedEntryArg
	if strings.ContainsRune(argv[0], '/') {
		path = argv[0]
	}
	if err := validateNativeEntry(path, helperPinnedExecutableFDArg); err != nil {
		return err
	}
	unix.CloseOnExec(readyFD)
	unix.CloseOnExec(releaseFD)
	unix.CloseOnExec(helperPinnedExecutableFDArg)
	if _, err := unix.Write(readyFD, []byte{1}); err != nil {
		return err
	}
	var release [1]byte
	n, err := unix.Read(releaseFD, release[:])
	if err != nil {
		return err
	}
	if n != 1 || release[0] != 1 {
		return syscall.EPROTO
	}
	_ = unix.Close(releaseFD)
	helperPinnedReleaseFDArg = -1
	// Repeat immediately before ordinary kernel lookup. This is a current identity
	// check, not an atomic pathname lookup or immutable inode-content guarantee.
	if err := validateNativeEntry(path, helperPinnedExecutableFDArg); err != nil {
		return err
	}
	_ = unix.Close(helperPinnedExecutableFDArg)
	helperPinnedExecutableFDArg = -1
	return unix.Exec(path, argv, os.Environ())
}
func validateNativeEntry(path string, fd int) error {
	var selected, current unix.Stat_t
	if err := unix.Fstat(fd, &selected); err != nil {
		return err
	}
	if err := unix.Stat(path, &current); err != nil {
		return err
	}
	if selected.Dev != current.Dev || selected.Ino != current.Ino {
		return ErrLaunchIdentityChanged
	}
	return nil
}
