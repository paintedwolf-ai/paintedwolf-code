//go:build linux

package linuxsandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAppliedSocketCreationFloor(t *testing.T) {
	if os.Getenv("PW_TEST_APPLIED_SOCKET_FILTER") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAppliedSocketCreationFloor$")
		cmd.Env = append(os.Environ(), "PW_TEST_APPLIED_SOCKET_FILTER=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("applied filter subprocess: %v\n%s", err, output)
		}
		return
	}
	// This child owns its filter; no restriction touches the test runner.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		t.Fatalf("no new privileges: %v", err)
	}
	listener, err := applySeccomp()
	if err != nil {
		t.Fatalf("apply production filter: %v", err)
	}
	defer func() { _ = unix.Close(listener) }()
	for _, flags := range []int{0, unix.SOCK_CLOEXEC, unix.SOCK_NONBLOCK, unix.SOCK_CLOEXEC | unix.SOCK_NONBLOCK} {
		for _, kind := range []int{unix.SOCK_DGRAM, unix.SOCK_RAW} {
			fd, err := unix.Socket(unix.AF_UNIX, kind|flags, 0)
			if err == nil {
				_ = unix.Close(fd)
			}
			if !errors.Is(err, unix.EACCES) {
				t.Fatalf("socket type %d flags %d: got %v, want EACCES", kind, flags, err)
			}
			pair, err := unix.Socketpair(unix.AF_UNIX, kind|flags, 0)
			if err == nil {
				_ = unix.Close(pair[0])
				_ = unix.Close(pair[1])
			}
			if !errors.Is(err, unix.EACCES) {
				t.Fatalf("socketpair type %d flags %d: got %v, want EACCES", kind, flags, err)
			}
		}
	}
	for _, family := range []int{unix.AF_VSOCK, unix.AF_BLUETOOTH, unix.AF_PACKET, unix.AF_TIPC, unix.AF_ALG} {
		fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err == nil {
			_ = unix.Close(fd)
		}
		if !errors.Is(err, unix.EAFNOSUPPORT) {
			t.Fatalf("socket family %d: got %v, want EAFNOSUPPORT", family, err)
		}
	}
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatalf("isolated socket family %d: %v", family, err)
		}
		_ = unix.Close(fd)
	}
	route, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		t.Fatalf("netlink route socket: %v", err)
	}
	_ = unix.Close(route)
	for _, kind := range []int{unix.SOCK_STREAM, unix.SOCK_SEQPACKET} {
		pair, err := unix.Socketpair(unix.AF_UNIX, kind|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatalf("permitted socketpair type %d: %v", kind, err)
		}
		_ = unix.Close(pair[0])
		_ = unix.Close(pair[1])
	}
}
