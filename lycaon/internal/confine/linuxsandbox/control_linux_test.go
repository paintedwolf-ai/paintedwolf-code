//go:build linux

package linuxsandbox

import (
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestControlCloseInterruptsDescriptorReceive(t *testing.T) {
	t.Parallel()
	for range 100 {
		parent, child, err := controlPair()
		if err != nil {
			t.Fatalf("create control pair: %v", err)
		}
		if err := parent.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("register control deadline: %v", err)
		}
		done := make(chan error, 1)
		go func() {
			_, _, err := receiveControl(parent, make([]byte, 16), make([]byte, unix.CmsgSpace(4)))
			done <- err
		}()
		if err := parent.Close(); err != nil {
			t.Fatalf("close control endpoint: %v", err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, os.ErrClosed) {
				t.Errorf("closed receive: got %v, want closed file", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("control receive outlived its closed endpoint")
		}
		if err := child.Close(); err != nil {
			t.Fatalf("close child endpoint: %v", err)
		}
	}
}

func TestControlPreservesDescriptorHandoffsAndCloseOnExec(t *testing.T) {
	t.Parallel()
	parent, child, err := controlPair()
	if err != nil {
		t.Fatalf("create control pair: %v", err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	t.Cleanup(func() { _ = child.Close() })
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatalf("open descriptor fixture: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if err := unix.Sendmsg(int(child.Fd()), []byte("handoff"), unix.UnixRights(int(file.Fd())), nil, 0); err != nil {
		t.Fatalf("send descriptor: %v", err)
	}
	header, rights := make([]byte, 16), make([]byte, unix.CmsgSpace(4))
	n, oobn, err := receiveControl(parent, header, rights)
	if err != nil {
		t.Fatalf("receive descriptor: %v", err)
	}
	fd, ok := receivedFD(rights[:oobn])
	if !ok {
		t.Fatal("handoff did not retain its descriptor")
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatalf("inspect descriptor flags: %v", err)
	}
	if string(header[:n]) != "handoff" || flags&unix.FD_CLOEXEC == 0 {
		t.Errorf("handoff changed: header=%q flags=%d", header[:n], flags)
	}
}

func TestControlWriteRacesSafelyWithClose(t *testing.T) {
	t.Parallel()
	for range 100 {
		parent, child, err := controlPair()
		if err != nil {
			t.Fatalf("create control pair: %v", err)
		}
		factory := newSocketFactory(parent)
		done := make(chan error, 1)
		go func() { done <- factory.send(controlMessage{Kind: controlReady}) }()
		_ = parent.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, os.ErrClosed) {
				t.Errorf("control send: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("control send outlived its closed endpoint")
		}
		_ = child.Close()
	}
}
