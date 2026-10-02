//go:build unix

package exec

import (
	"context"
	"os"
	"os/exec"
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
	inherited, err := processNice(os.Getpid())
	testutil.FailErr(t, "read the test's nice value", err)
	nice, err := processNice(cmd.Process.Pid)
	testutil.FailErr(t, "read the child's nice value", err)
	if want := max(inherited, DefaultBelowNormalNice); nice != want {
		t.Fatalf("nice = %d want %d (inherited %d)", nice, want, inherited)
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
