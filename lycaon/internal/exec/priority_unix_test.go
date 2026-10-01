//go:build unix

package exec

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPriorityBelowNormalSetsNice(t *testing.T) {
	guard, err := newRunGuard(ProcessPriorityBelowNormal)
	testutil.FailErr(t, "newRunGuard failed", err)
	defer guard.release()

	cmd := exec.CommandContext(context.Background(), "sleep", "2")
	guard.configure(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatal("expected Setsid")
	}
	if err := cmd.Start(); err != nil {
		testutil.FailErr(t, "cmd.Start failed", err)
	}
	defer func() {
		guard.kill(cmd)
		_ = cmd.Wait()
	}()
	if err := guard.onStarted(cmd); err != nil {
		t.Fatalf("onStarted: %v", err)
	}
	// Give the kernel a beat to apply priority.
	time.Sleep(20 * time.Millisecond)
	nice, err := syscall.Getpriority(syscall.PRIO_PROCESS, cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Getpriority: %v", err)
	}
	if nice != DefaultBelowNormalNice {
		t.Fatalf("nice = %d want %d", nice, DefaultBelowNormalNice)
	}
}

func TestPriorityNormalLeavesDefault(t *testing.T) {
	guard, err := newRunGuard(ProcessPriorityNormal)
	testutil.FailErr(t, "newRunGuard failed", err)
	defer guard.release()
	cmd := exec.CommandContext(context.Background(), "sleep", "1")
	guard.configure(cmd)
	if err := cmd.Start(); err != nil {
		testutil.FailErr(t, "cmd.Start failed", err)
	}
	defer func() {
		guard.kill(cmd)
		_ = cmd.Wait()
	}()
	if err := guard.onStarted(cmd); err != nil {
		t.Fatalf("onStarted: %v", err)
	}
}

// A child that exits before it can be niced has already done its work.
func TestPriorityIgnoresAlreadyExitedChild(t *testing.T) {
	guard, err := newRunGuard(ProcessPriorityBelowNormal)
	testutil.FailErr(t, "newRunGuard failed", err)
	defer guard.release()

	cmd := exec.CommandContext(context.Background(), "true")
	guard.configure(cmd)
	if err := cmd.Start(); err != nil {
		testutil.FailErr(t, "cmd.Start failed", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := guard.onStarted(cmd); err != nil {
		t.Fatalf("onStarted after exit = %v, want nil", err)
	}
}
