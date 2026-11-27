//go:build linux || darwin

package githelper

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPeerKernelFactsAndAncestry(t *testing.T) {
	parent, err := parentProcess(os.Getpid())
	testutil.FailErr(t, "parent process", err)
	if parent != os.Getppid() {
		t.Fatalf("parent process = %d, want %d", parent, os.Getppid())
	}
	exe, err := processExecutable(os.Getpid())
	testutil.FailErr(t, "process executable", err)
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)
	if !sameFile(exe, self) {
		t.Fatalf("executable = %q, want %q", exe, self)
	}
	if _, err := parentProcess(1 << 22); err == nil {
		t.Fatal("absent process had a parent")
	}

	// PID reuse changes start time and must refuse ancestry match.
	parentStart, ok := osprocess.StartTime(parent)
	if !ok {
		t.Fatal("missing parent start time")
	}
	parentExe, err := processExecutable(parent)
	testutil.FailErr(t, "parent executable", err)
	if err := descendsFromServedGit(os.Getpid(), parentExe, ServedProcess{PID: parent, Start: parentStart}); err != nil {
		t.Fatalf("matching parent ancestry failed: %v", err)
	}
	if err := descendsFromServedGit(os.Getpid(), parentExe, ServedProcess{PID: parent, Start: parentStart + 1}); err == nil {
		t.Fatal("mismatched start time was accepted as the served process")
	}
}

func TestBindingValidationFailedBindAndCleanup(t *testing.T) {
	if _, err := NewBinding(""); err == nil {
		t.Fatal("empty git binary was accepted")
	}
	if _, err := NewBinding("/missing/git-binary"); err == nil {
		t.Fatal("missing git binary was accepted")
	}
	if _, err := Dial(""); err == nil {
		t.Fatal("empty socket path was accepted")
	}
	if _, err := Dial("relative/socket"); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Fatalf("relative socket path = %v", err)
	}

	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)
	binding, err := NewBinding(self)
	testutil.FailErr(t, "new binding", err)

	exited := exec.Command("true")
	testutil.FailErr(t, "run exited process", exited.Run())
	if err := binding.Bind(exited.Process.Pid); err == nil {
		t.Fatal("bound an exited process")
	}
	if binding.BindError() == nil {
		t.Fatal("failed bind recorded no BindError")
	}
	if err := binding.Bind(os.Getpid()); err == nil {
		t.Fatal("rebound after failed bind")
	}

	ln, dir, err := Listen(t.Context(), "probe")
	testutil.FailErr(t, "listen", err)
	socketPath := filepath.Join(dir, "probe")
	clientDone := make(chan error, 1)
	unverifiedBinding, err := NewBinding(self)
	testutil.FailErr(t, "new binding", err)
	go func() {
		conn, acceptErr := ln.AcceptUnix()
		if acceptErr != nil {
			clientDone <- acceptErr
			return
		}
		defer func() { _ = conn.Close() }()
		_, verifyErr := unverifiedBinding.Verify(context.Background(), conn, time.Now().Add(5*time.Second))
		clientDone <- verifyErr
	}()
	dialed, err := Dial(socketPath)
	testutil.FailErr(t, "dial", err)
	defer func() { _ = dialed.Close() }()
	unverifiedBinding.Close()
	if verifyErr := <-clientDone; !errors.Is(verifyErr, ErrClosed) {
		t.Fatalf("Verify after Close = %v, want %v", verifyErr, ErrClosed)
	}
	if unverifiedBinding.BindError() != nil {
		t.Fatalf("Close before Bind recorded BindError = %v", unverifiedBinding.BindError())
	}
	_ = ln.Close()
	_ = os.RemoveAll(dir)
}
