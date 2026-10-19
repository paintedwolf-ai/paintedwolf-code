//go:build linux

package confine_test

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const unshareProbeFlags = "PW_CONFINE_UNSHARE_FLAGS"

func TestLinuxUnshareProbeProcess(t *testing.T) {
	value := os.Getenv(unshareProbeFlags)
	if value == "" {
		return
	}
	flags, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		os.Exit(2)
	}
	_, _, errno := unix.RawSyscall(unix.SYS_UNSHARE, uintptr(flags), 0, 0)
	fmt.Println(errno)
	if errno != unix.EPERM {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestLinuxFilesystemUnshareRefusesWithoutChangingNamespacePolicy(t *testing.T) {
	self := requireLinuxSandbox(t)
	for _, flags := range []uint64{unix.CLONE_FS, unix.CLONE_NEWNS, unix.CLONE_FS | unix.CLONE_NEWUSER, unix.CLONE_FS | 1<<32} {
		t.Run(strconv.FormatUint(flags, 10), func(t *testing.T) {
			env := append(os.Environ(), unshareProbeFlags+"="+strconv.FormatUint(flags, 10))
			status, output := linuxConfinedRun(t, self, denyConfinement(t.TempDir()), env, self, "-test.run=^TestLinuxUnshareProbeProcess$")
			if flags == unix.CLONE_FS {
				if status.ExitStatus() != 0 || status.Signaled() {
					t.Fatalf("filesystem unshare did not return EPERM: %v %s", status, output)
				}
			} else if !status.Signaled() || status.Signal() != syscall.SIGSYS {
				t.Fatalf("namespace or unknown unshare escaped the kill policy: %v %s", status, output)
			}
		})
	}
}
