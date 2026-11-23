//go:build linux

package linuxsandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestAppliedSocketCreationFloor(t *testing.T) {
	if os.Getenv("PW_TEST_APPLIED_SOCKET_FILTER") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAppliedSocketCreationFloor$")
		cmd.Env = append(os.Environ(), "PW_TEST_APPLIED_SOCKET_FILTER=1")
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("applied filter subprocess: %v\n%s", err, output)
		}
		return
	}
	// This child owns its filter; no restriction touches the test runner.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		t.Fatalf("no new privileges: %v", err)
	}
	listener, err := applySeccomp(false)
	if err != nil {
		t.Fatalf("apply production filter: %v", err)
	}
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
	_ = unix.Close(listener)
	os.Exit(0)
}

func TestObservedOpenFilterDeliversZeroNotificationsPerFDRead(t *testing.T) {
	const childEnv = "PW_TEST_OBSERVED_OPEN_NOTIF_CHILD"
	const targetEnv = "PW_TEST_OBSERVED_OPEN_TARGET"
	if os.Getenv(childEnv) == "1" {
		target := os.Getenv(targetEnv)
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			os.Exit(2)
		}
		listener, err := applySeccomp(true)
		if err != nil {
			os.Exit(3)
		}
		rights := unix.UnixRights(listener)
		if err := unix.Sendmsg(3, []byte{1}, rights, nil, 0); err != nil {
			os.Exit(4)
		}
		_ = unix.Close(listener)
		_ = unix.Close(3)
		const opens = 4
		buf := make([]byte, 16)
		for range opens {
			pathFD, err := unix.Openat(unix.AT_FDCWD, target, unix.O_PATH|unix.O_CLOEXEC, 0)
			if err != nil {
				os.Exit(5)
			}
			_ = unix.Close(pathFD)
			fd, err := unix.Openat(unix.AT_FDCWD, target, unix.O_RDONLY|unix.O_CLOEXEC, 0)
			if err != nil {
				os.Exit(6)
			}
			for range 250 {
				if _, err := unix.Pread(fd, buf, 0); err != nil {
					os.Exit(7)
				}
			}
			_ = unix.Close(fd)
		}
		os.Exit(0)
	}

	target := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(target, []byte("hello-shared-temp"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	defer func() { _ = unix.Close(pair[0]) }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := os.NewFile(uintptr(pair[1]), "notif-sock")
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestObservedOpenFilterDeliversZeroNotificationsPerFDRead$")
	cmd.Env = append(os.Environ(), childEnv+"=1", targetEnv+"="+target)
	cmd.ExtraFiles = []*os.File{child}
	if err := cmd.Start(); err != nil {
		_ = child.Close()
		t.Fatalf("start child: %v", err)
	}
	_ = child.Close()

	msg, oob := make([]byte, 8), make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, err := unix.Recvmsg(pair[0], msg, oob, unix.MSG_CMSG_CLOEXEC)
	if err != nil {
		t.Fatalf("recv listener: %v", err)
	}
	fds := receivedFDs(oob[:oobn])
	if len(fds) != 1 {
		t.Fatalf("received %d fds, want 1", len(fds))
	}
	listener := fds[0]
	var openNotifs, otherNotifs atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = unix.Close(listener) }()
		for {
			var n seccompNotif
			if err := ioctlPtr(listener, unix.SECCOMP_IOCTL_NOTIF_RECV, unsafe.Pointer(&n)); err != nil {
				if errors.Is(err, unix.EINTR) {
					continue
				}
				if errors.Is(err, unix.ENOENT) && !listenerClosed(listener) {
					continue
				}
				return
			}
			if uint32(n.Nr) == unix.SYS_OPENAT || (legacyOpen != 0 && uint32(n.Nr) == legacyOpen) {
				openNotifs.Add(1)
			} else {
				otherNotifs.Add(1)
			}
			resp := seccompNotifResp{ID: n.ID, Flags: unix.SECCOMP_USER_NOTIF_FLAG_CONTINUE}
			_ = ioctlPtr(listener, unix.SECCOMP_IOCTL_NOTIF_SEND, unsafe.Pointer(&resp))
		}
	}()

	if err := cmd.Wait(); err != nil {
		t.Fatalf("child failed: %v", err)
	}
	<-done
	if got := openNotifs.Load(); got != 4 {
		t.Fatalf("open notifications = %d, want 4 across 4 O_RDONLY opens, 4 O_PATH opens, and 1000 pread calls", got)
	}
	if got := otherNotifs.Load(); got != 0 {
		t.Fatalf("unexpected non-open notifications = %d", got)
	}
}
