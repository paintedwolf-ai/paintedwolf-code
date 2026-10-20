//go:build linux

package confine

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestNativeGateBindsKernelSenderAndSameMessageProcessDescriptor(t *testing.T) {
	host, child, err := nativeGatePair()
	testutil.FailErr(t, "create launch gate", err)
	defer host.Close()
	defer child.Close()
	testutil.FailErr(t, "send ready with self pidfd", sendNativeGateReady(int(child.Fd()), 17))
	ready, err := receiveNativeGateReady(t.Context(), int(host.Fd()))
	testutil.FailErr(t, "receive native sender identity", err)
	defer ready.Close()
	if ready.PID != os.Getpid() || ready.ExecutableFD != 17 || ready.Credentials.Uid != uint32(os.Geteuid()) || ready.StartTime == 0 || ready.Namespaces[0] == "" {
		t.Fatal("ready identity lost actual kernel facts")
	}
	testutil.FailErr(t, "validate retained process descriptor", verifyGatePidfd(int(ready.Process.Fd()), os.Getpid()))
	if err := verifyGatePidfd(int(ready.Process.Fd()), os.Getpid()+1); !errors.Is(err, osprocess.ErrExecutableObservationUnavailable) {
		t.Fatal("a different numeric PID was accepted")
	}
}

func TestNativeGateRejectsMissingFakeAndExtraProcessDescriptors(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			host, child, err := nativeGatePair()
			testutil.FailErr(t, "create gate", err)
			defer host.Close()
			defer child.Close()
			file, err := os.Open("/dev/null")
			testutil.FailErr(t, "open non-process descriptor", err)
			defer file.Close()
			var packet [nativeGateReadyBytes]byte
			packet[0], packet[1] = nativeGateVersion, 1
			rights := make([]int, count)
			for i := range rights {
				rights[i] = int(file.Fd())
			}
			var ancillary []byte
			if count > 0 {
				ancillary = unix.UnixRights(rights...)
			}
			testutil.FailErr(t, "send invalid process transport", unix.Sendmsg(int(child.Fd()), packet[:], ancillary, nil, 0))
			ready, err := receiveNativeGateReady(t.Context(), int(host.Fd()))
			ready.Close()
			if err == nil {
				t.Fatal("invalid process descriptor transport accepted")
			}
		})
	}
}

func TestNativeGateCancellationExecFailureAndEOFStayDistinct(t *testing.T) {
	host, child, err := nativeGatePair()
	testutil.FailErr(t, "create gate", err)
	defer host.Close()
	defer child.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := receiveNativeGateReady(ctx, int(host.Fd())); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled gate waited or accepted")
	}
	testutil.FailErr(t, "send native error", unix.Sendmsg(int(child.Fd()), []byte{1, 2, byte(syscall.ENOEXEC), 0, 0, 0}, nil, nil, 0))
	if err := awaitNativeExec(t.Context(), int(host.Fd())); !errors.Is(err, syscall.ENOEXEC) {
		t.Fatal("native errno became a successful handoff")
	}
	testutil.FailErr(t, "close sender", child.Close())
	timeout, finish := context.WithTimeout(t.Context(), time.Second)
	defer finish()
	if _, err := receiveNativeGateReady(timeout, int(host.Fd())); err == nil {
		t.Fatal("EOF before authenticated ready accepted")
	}
}

func TestHeldExecutablePathUsesActualDescriptorAndRejectsClosedInput(t *testing.T) {
	self, err := os.Executable()
	testutil.FailErr(t, "locate fixture image", err)
	file, err := os.Open(self)
	testutil.FailErr(t, "hold fixture image", err)
	path, err := heldExecutablePath(int(file.Fd()))
	testutil.FailErr(t, "verify native procfs descriptor route", err)
	actual, err := os.Stat(path)
	testutil.FailErr(t, "observe descriptor route", err)
	held, err := file.Stat()
	testutil.FailErr(t, "observe held image", err)
	if !os.SameFile(actual, held) {
		t.Fatal("descriptor route changed selected image")
	}
	fd := int(file.Fd())
	testutil.FailErr(t, "close held image", file.Close())
	if _, err := heldExecutablePath(fd); err == nil {
		t.Fatal("closed image admitted")
	}
}
