package bgprocess_test

import (
	"context"
	"runtime"
	"testing"

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
		Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
	}
	handle, err := startBackground(context.Background(), reg, "sess-1", "proj-1", req, runner)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	released := make(chan struct{})
	reg.Lifecycle.OnExit("sess-1", handle, func() { close(released) })

	out, err := reg.Output.ReadOutput(t.Context(), "sess-1", handle)
	if err != nil {
		t.Fatalf("ReadOutput: %v", err)
	}
	if !out.Running {
		t.Fatal("expected process to still be running")
	}
	select {
	case <-released:
		t.Fatal("released before the process exited")
	default:
	}
	if _, err := reg.Lifecycle.Stop("sess-1", handle, bgprocess.TerminationStoppedByAgent); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-released:
	case <-t.Context().Done():
		t.Fatal("never released after the process exited")
	}
}

// An unknown handle must still release, or a failed start would strand the token.
func TestOnExitFiresImmediatelyForUnknownHandle(t *testing.T) {
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	released := make(chan struct{})
	reg.Lifecycle.OnExit("sess-1", "no-such-handle", func() { close(released) })
	select {
	case <-released:
	case <-t.Context().Done():
		t.Fatal("unknown handle must release immediately")
	}
}
