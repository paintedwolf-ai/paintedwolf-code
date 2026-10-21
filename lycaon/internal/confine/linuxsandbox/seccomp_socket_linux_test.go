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

func TestAppliedUnixDatagramSpellings(t *testing.T) {
	if os.Getenv("PW_TEST_APPLIED_DATAGRAM_FILTER") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAppliedUnixDatagramSpellings$")
		cmd.Env = append(os.Environ(), "PW_TEST_APPLIED_DATAGRAM_FILTER=1")
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
	for _, kind := range []int{unix.SOCK_STREAM, unix.SOCK_SEQPACKET} {
		pair, err := unix.Socketpair(unix.AF_UNIX, kind|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatalf("permitted socketpair type %d: %v", kind, err)
		}
		_ = unix.Close(pair[0])
		_ = unix.Close(pair[1])
	}
}
