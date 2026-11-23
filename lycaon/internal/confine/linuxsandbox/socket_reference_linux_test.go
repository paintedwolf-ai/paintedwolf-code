//go:build linux

package linuxsandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox/sessionipc"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func referenceListener(t *testing.T, path string, kind int) int {
	t.Helper()
	fd, err := unix.Socket(unix.AF_UNIX, kind|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create listener", err)
	t.Cleanup(func() { _ = unix.Close(fd) })
	testutil.FailErr(t, "bind listener", unix.Bind(fd, &unix.SockaddrUnix{Name: path}))
	testutil.FailErr(t, "listen", unix.Listen(fd, 8))
	return fd
}

func TestSupervisorSocketReferenceSurvivesRetirementAndDescriptorReuse(t *testing.T) {
	for _, kind := range []int{unix.SOCK_STREAM, unix.SOCK_SEQPACKET} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "sd-") //nolint:usetesting // Kernel Unix socket addresses are bounded.
			testutil.FailErr(t, "reference fixture directory", err)
			defer os.RemoveAll(root)
			path := filepath.Join(root, "socket")
			original := referenceListener(t, path, kind)
			fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
			testutil.FailErr(t, "retain original", err)
			network := Network{SocketGrants: []SocketGrant{{Path: path, FD: fd}}}
			refs, err := retainSocketReferences(network)
			testutil.FailErr(t, "supervisor owns original", err)
			defer refs.close()
			paused, resume := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-resume:
				default:
					close(resume)
				}
			}()
			network.beforeSocketConnect = func() { close(paused); <-resume }
			s := &supervisor{network: network, socketRefs: refs}
			client, err := unix.Socket(unix.AF_UNIX, kind|unix.SOCK_CLOEXEC, 0)
			testutil.FailErr(t, "create connecting socket", err)
			defer unix.Close(client)
			completed := make(chan unix.Errno, 1)
			go func() { completed <- s.connectUnix(client, fd) }()
			select {
			case <-paused:
			case err := <-completed:
				t.Fatalf("supervisor did not reach pause: %v", err)
			case <-t.Context().Done():
				t.Fatal("socket operation was canceled before pause")
			}
			refs.close()
			testutil.FailErr(t, "move original name", os.Rename(path, path+"-original"))
			decoy := referenceListener(t, path, kind)
			decoyRef, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
			testutil.FailErr(t, "retain decoy fixture", err)
			defer unix.Close(decoyRef)
			testutil.FailErr(t, "close inherited original", unix.Close(fd))
			if decoyRef != fd {
				testutil.FailErr(t, "recycle inherited descriptor", unix.Dup3(decoyRef, fd, unix.O_CLOEXEC))
				defer unix.Close(fd)
			}
			close(resume)
			if errno := <-completed; errno != 0 {
				t.Fatalf("admitted operation after retirement: %v", errno)
			}
			_, err = unix.Write(client, []byte("original-only"))
			testutil.FailErr(t, "write original marker", err)
			accepted, _, err := unix.Accept4(original, unix.SOCK_CLOEXEC)
			testutil.FailErr(t, "original accepts", err)
			defer unix.Close(accepted)
			bytes := make([]byte, 32)
			n, err := unix.Read(accepted, bytes)
			testutil.FailErr(t, "original reads", err)
			if string(bytes[:n]) != "original-only" {
				t.Fatalf("original bytes = %q", bytes[:n])
			}
			ready, err := unix.Poll([]unix.PollFd{{Fd: int32(decoy), Events: unix.POLLIN}}, 0)
			testutil.FailErr(t, "decoy readiness", err)
			if ready != 0 {
				t.Fatal("recycled descriptor redirected connection to decoy")
			}
			late, err := unix.Socket(unix.AF_UNIX, kind|unix.SOCK_CLOEXEC, 0)
			testutil.FailErr(t, "late connecting socket", err)
			defer unix.Close(late)
			if errno := s.connectUnix(late, fd); errno != unix.EACCES {
				t.Fatalf("late worker = %v", errno)
			}
			if _, err := refs.duplicate(fd); !errors.Is(err, unix.EACCES) {
				t.Fatalf("retired reference duplicated: %v", err)
			}
		})
	}
}

func TestPerformerBindsOnlyRegisteredSessionSockets(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "protected")
	client := startTestPerformer(t, Filesystem{ReadOnly: []string{blocked}})
	var registry sessionipc.Registry
	channel, err := registry.Channel(root)
	testutil.FailErr(t, "open session channel", err)
	defer func() { _ = channel.Close() }()
	socket, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create session socket", err)
	defer func() { _ = unix.Close(socket) }()
	bind := func(path string) pathResult {
		t.Helper()
		result, _, err := client.call(pathCall{Op: callSessionBind, Path: path, NewPath: root, Dirs: 2}, []int{socket, int(channel.Fd())})
		testutil.FailErr(t, "submit session bind", err)
		return result
	}
	for _, path := range []string{blocked, filepath.Join(t.TempDir(), "other")} {
		result := bind(path)
		if result.Errno != int(unix.EACCES) || result.Refusal == nil || result.Refusal.Operation != "network-bind" || result.Refusal.Target != path {
			t.Fatalf("bind outside session authority %s: %+v", path, result)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refused bind created %s: %v", path, err)
		}
	}
	occupied := filepath.Join(root, "occupied")
	testutil.FailErr(t, "occupy name", os.WriteFile(occupied, []byte("entry"), 0600))
	if result := bind(occupied); result.Errno != int(unix.EADDRINUSE) || result.Refusal != nil {
		t.Fatalf("occupied name: %+v", result)
	}
	path := filepath.Join(root, "@endpoint")
	result := bind(path)
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	testutil.FailErr(t, "pin bound filesystem socket", err)
	defer func() { _ = unix.Close(fd) }()
	// Linux requires CAP_NET_ADMIN in the socket's network namespace to pin the bound inode.
	pin, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(socket), 0x89e0, 0)
	if errno == 0 {
		_ = unix.Close(int(pin))
	}
	if result.Errno != int(errno) || sessionipc.Admits(int(channel.Fd()), fd) != (errno == 0) {
		t.Fatalf("registered bind: result %+v, kernel pin error %v", result, errno)
	}
}
