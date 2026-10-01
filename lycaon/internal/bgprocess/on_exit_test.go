package bgprocess_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
)

// Process-scoped resources stay live until process exit.
func TestOnExitFiresAfterProcessExits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	req := hostcmd.Request{
		Launch:     exec.HostLaunch("on-exit test"),
		ProjectDir: t.TempDir(),
		Stages:     []exec.Stage{{Name: "sh", Args: []string{"-c", "sleep 0.2"}}},
	}
	handle, err := startBackground(context.Background(), reg, "sess-1", "proj-1", req, runner)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	released := make(chan struct{})
	reg.OnExit("sess-1", handle, func() { close(released) })

	select {
	case <-released:
		t.Fatal("released before the process exited")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-released:
	case <-time.After(10 * time.Second):
		t.Fatal("never released after the process exited")
	}
}

// An unknown handle must still release, or a failed start would strand the token.
func TestOnExitFiresImmediatelyForUnknownHandle(t *testing.T) {
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	released := make(chan struct{})
	reg.OnExit("sess-1", "no-such-handle", func() { close(released) })
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("unknown handle must release immediately")
	}
}
