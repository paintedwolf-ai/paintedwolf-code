//go:build darwin || linux

package exec

import (
	"context"
	"github.com/lycaon/lycaon/internal/confine"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOwnedProcessGroupWaitsOnceStopsTreeAndPreservesUnrelatedProcess(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	script := "sleep 30 & echo $! > child.pid; wait"
	cmd, cleanup, err := PrepareCommand(ctx, "/bin/sh", []string{"-c", script}, ExecOpts{Launch: HostLaunch("synthetic asynchronous group"), Dir: root})
	testutil.FailErr(t, "prepare asynchronous group", err)
	defer cleanup()
	group, err := StartInOwnGroup(cmd)
	testutil.FailErr(t, "start owned tree", err)
	defer func() { group.Stop(); _ = group.Wait() }()
	child := waitForPIDFile(t, filepath.Join(root, "child.pid"))
	settled := make(chan error, 2)
	for range 2 {
		go func() { settled <- group.Wait() }()
	}
	group.Stop()
	for range 2 {
		select {
		case err := <-settled:
			if err == nil {
				t.Fatal("killed native group reported success")
			}
		case <-ctx.Done():
			t.Fatal("concurrent native Wait did not settle")
		}
	}
	testutil.FailErr(t, "owned descendant survived", waitForProcessGone(child, 5*time.Second))
	unrelated := exec.Command("/bin/sleep", "30")
	testutil.FailErr(t, "start unrelated process", unrelated.Start())
	defer func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() }()
	before, ok := osprocess.StartTime(unrelated.Process.Pid)
	if !ok {
		t.Fatal("unrelated native identity absent")
	}
	group.Stop()
	after, current := osprocess.StartTime(unrelated.Process.Pid)
	if !current || before != after || syscall.Kill(unrelated.Process.Pid, 0) != nil {
		t.Fatal("settled group stop signalled unrelated process")
	}
}

func TestOwnedProcessGroupCancellationRetainsNativeSettlement(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd, cleanup, err := PrepareCommand(ctx, "/bin/sh", []string{"-c", "echo $$ > leader.pid; sleep 30"}, ExecOpts{Launch: HostLaunch("synthetic asynchronous cancellation"), Dir: root})
	testutil.FailErr(t, "prepare owned cancellation", err)
	defer cleanup()
	group, err := StartInOwnGroup(cmd)
	testutil.FailErr(t, "start cancellation fixture", err)
	defer func() { group.Stop(); _ = group.Wait() }()
	leader := waitForPIDFile(t, filepath.Join(root, "leader.pid"))
	done := make(chan error, 1)
	go func() { done <- group.Wait() }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled native group reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled native owner did not settle")
	}
	testutil.FailErr(t, "cancelled leader survived", waitForProcessGone(leader, 5*time.Second))
	if cmd.ProcessState == nil {
		t.Fatal("native Wait state was discarded")
	}
}

func TestOwnedProcessGroupRefusesConsumedPreparedCommand(t *testing.T) {
	cmd, cleanup, err := PrepareCommand(t.Context(), "/bin/echo", []string{"literal"}, ExecOpts{Launch: HostLaunch("synthetic consumed group")})
	testutil.FailErr(t, "prepare once-only group", err)
	defer cleanup()
	group, err := StartInOwnGroup(cmd)
	testutil.FailErr(t, "start first native group", err)
	testutil.FailErr(t, "settle first native group", group.Wait())
	if second, err := StartInOwnGroup(cmd); err == nil || second != nil {
		t.Fatal("consumed native command started twice")
	}
}

func TestPreparedGroupRefusesRemovedCaptureAfterOwnerRegistration(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd, cleanup, err := PrepareCommand(ctx, "/bin/sh", []string{"-c", "printf forbidden > effect"}, ExecOpts{Launch: HostLaunch("synthetic removed handoff"), Dir: root})
	testutil.FailErr(t, "prepare gated native command", err)
	defer cleanup()
	cmd, supervisorCleanup, err := superviseCommand(cmd, func() {}, ProcessPriorityNormal)
	testutil.FailErr(t, "select existing supervisor", err)
	defer supervisorCleanup()
	guard, err := newRunGuard(ProcessPriorityNormal)
	testutil.FailErr(t, "create actual native tree owner", err)
	defer guard.release()
	guard.configure(cmd)
	testutil.FailErr(t, "validate captured inputs", confine.ValidateProcessInputs(cmd))
	testutil.FailErr(t, "start held native helper", cmd.Start())
	settled := false
	defer func() {
		if !settled {
			guard.kill(cmd)
			_ = cmd.Wait()
		}
	}()
	testutil.FailErr(t, "register native owner without releasing payload", guard.onStarted(cmd))
	if _, err := os.Stat(filepath.Join(root, "effect")); !os.IsNotExist(err) {
		t.Fatal("owner registration released payload")
	}
	removed := make(chan struct{})
	go func() { cleanup(); close(removed) }()
	<-removed
	if confine.ConfirmProcessLaunch(cmd) == nil {
		t.Fatal("removed capture reported accepted handoff")
	}
	guard.kill(cmd)
	_ = cmd.Wait()
	settled = true
	if _, err := os.Stat(filepath.Join(root, "effect")); !os.IsNotExist(err) {
		t.Fatal("removed capture released payload")
	}
}
