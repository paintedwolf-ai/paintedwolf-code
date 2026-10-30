//go:build linux

package socketref

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestInvocationCaptureCannotChangeDuringRepeatedPreflight(t *testing.T) {
	path := filepath.Join(shortSocketRoot(t), "socket")
	original, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	testutil.FailErr(t, "listen on fixture Unix socket", err)
	original.SetUnlinkOnClose(false)
	defer original.Close()
	ctx, scope := WithScope(t.Context())
	defer scope.Close()
	first, err := CaptureForInvocation(ctx, path)
	testutil.FailErr(t, "capture invocation socket", err)
	if err := os.Remove(path); err != nil {
		testutil.FailErr(t, "remove socket path", err)
	}
	decoy, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	testutil.FailErr(t, "listen on fixture Unix socket", err)
	defer decoy.Close()
	for range 128 {
		next, err := CaptureForInvocation(ctx, path)
		if err != nil || next != first {
			t.Fatalf("repeated capture changed: %v, %v", next, err)
		}
	}
	acceptedOwner, err := first.Acquire()
	testutil.FailErr(t, "acquire captured binding", err)
	scope.Close()
	if _, err := CaptureForInvocation(ctx, path); !errors.Is(err, ErrUnbound) {
		t.Fatalf("closed scope captured: %v", err)
	}
	client, err := acceptedOwner.DialContext(t.Context())
	testutil.FailErr(t, "dial accepted socket owner", err)
	defer client.Close()
	accepted, err := original.AcceptUnix()
	testutil.FailErr(t, "accept original socket connection", err)
	_ = accepted.Close()
	acceptedOwner.Close()
	if _, err := first.Acquire(); !errors.Is(err, ErrUnbound) {
		t.Fatalf("last released binding acquired: %v", err)
	}
}

func TestInvocationAdoptsOnlySameRetainedObject(t *testing.T) {
	path := filepath.Join(shortSocketRoot(t), "socket")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	testutil.FailErr(t, "listen on fixture Unix socket", err)
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	saved, err := Capture(path)
	testutil.FailErr(t, "capture socket reference", err)
	defer saved.Close()
	ctx, scope := WithScope(t.Context())
	defer scope.Close()
	initial, err := CaptureForInvocation(ctx, path)
	testutil.FailErr(t, "capture invocation socket", err)
	if initial == saved.Binding() {
		t.Fatal("distinct captures share an authority ID")
	}
	if !ReuseForInvocation(ctx, path, saved.Binding()) {
		t.Fatal("same retained object was refused")
	}
	adopted, err := CaptureForInvocation(ctx, path)
	if err != nil || adopted != saved.Binding() {
		t.Fatalf("adopted capture = %v, %v", adopted, err)
	}
	if _, err := initial.Acquire(); !errors.Is(err, ErrUnbound) {
		t.Fatalf("transient capture retained after transfer: %v", err)
	}
	if err := os.Remove(path); err != nil {
		testutil.FailErr(t, "remove socket path", err)
	}
	decoy, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	testutil.FailErr(t, "listen on fixture Unix socket", err)
	defer decoy.Close()
	otherCtx, otherScope := WithScope(context.Background())
	defer otherScope.Close()
	if _, err := CaptureForInvocation(otherCtx, path); err != nil {
		testutil.FailErr(t, "capture invocation socket", err)
	}
	if ReuseForInvocation(otherCtx, path, saved.Binding()) {
		t.Fatal("replacement inherited saved authority")
	}
}
