//go:build darwin && cgo

package confine

/*
#include <spawn.h>
#include <sys/wait.h>
#include <sys/sysctl.h>
#include <errno.h>
#include <stdlib.h>

static int pw_spawn_suspended(const char *path, char *const argv[], char *const env[]) {
 posix_spawnattr_t attr;
 int err = posix_spawnattr_init(&attr);
 if (err) return err;
 err = posix_spawnattr_setflags(&attr, POSIX_SPAWN_SETEXEC | POSIX_SPAWN_START_SUSPENDED);
 if (!err) {
  pid_t pid;
  err = posix_spawn(&pid, path, NULL, &attr, argv, env);
 }
 posix_spawnattr_destroy(&attr);
 return err ? err : EIO; // SETEXEC success cannot return to this image.
}
static int pw_probe_stopped(int pid) {
 siginfo_t status = {0};
 int err = waitid(P_PID, (id_t)pid, &status, WSTOPPED | WEXITED | WNOWAIT | WNOHANG);
 if (err) return -errno;
 if (!status.si_pid) return 0;
 return status.si_code == CLD_STOPPED ? 1 : -ECHILD;
}
static int pw_native_argmax(int *maximum) {
 size_t length = sizeof(*maximum);
 return sysctlbyname("kern.argmax", maximum, &length, NULL, 0) ? errno : 0;
}
*/
import "C"

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/lycaon/lycaon/internal/osprocess"
	"golang.org/x/sys/unix"
)

func pinnedPlatformAvailable() error { return nil }
func nativeArgumentBudget() (int, error) {
	var maximum C.int
	if code := C.pw_native_argmax(&maximum); code != 0 {
		return 0, syscall.Errno(code)
	}
	if maximum <= 0 || maximum > 32<<20 {
		return 0, osprocess.ErrExecutableObservationUnavailable
	}
	return int(maximum), nil
}
func awaitPinnedStop(ctx context.Context, pid int) error {
	for {
		switch state := int(C.pw_probe_stopped(C.int(pid))); {
		case state == 1:
			return nil
		case state < 0:
			return fmt.Errorf("%w: %w", osprocess.ErrExecutableObservationUnavailable, syscall.Errno(-state))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}
func resumePinnedProcess(process *os.Process) error { return process.Signal(syscall.SIGCONT) }

func execPinnedTarget(path string, argv []string, cwdFD, readyFD int) error {
	if err := unix.Fchdir(cwdFD); err != nil {
		return err
	}
	_ = unix.Close(cwdFD)
	syscall.CloseOnExec(readyFD)
	if _, err := unix.Write(readyFD, []byte{1}); err != nil {
		return err
	}
	args, freeArgs, err := nativeCStringVector(argv)
	if err != nil {
		return err
	}
	defer freeArgs()
	env, freeEnv, err := nativeCStringVector(os.Environ())
	if err != nil {
		return err
	}
	defer freeEnv()
	nativePath := C.CString(path)
	defer C.free(unsafe.Pointer(nativePath))
	code := C.pw_spawn_suspended(nativePath, &args[0], &env[0])
	if code == 0 {
		return errors.New("suspended SETEXEC returned unexpectedly")
	}
	var message [5]byte
	message[0] = 2
	binary.LittleEndian.PutUint32(message[1:], uint32(code))
	// Replace the ready prefix with a typed exec failure after a failed syscall.
	// The reader accepts this bounded final record without interpreting stderr.
	_, _ = unix.Write(readyFD, message[:])
	return syscall.Errno(code)
}
func nativeCStringVector(words []string) ([]*C.char, func(), error) {
	out := make([]*C.char, len(words)+1)
	for i, word := range words {
		if strings.IndexByte(word, 0) >= 0 {
			err := syscall.EINVAL
			for _, previous := range out {
				if previous != nil {
					C.free(unsafe.Pointer(previous))
				}
			}
			return nil, nil, err
		}
		out[i] = C.CString(word)
	}
	return out, func() {
		for _, word := range out {
			if word != nil {
				C.free(unsafe.Pointer(word))
			}
		}
	}, nil
}
