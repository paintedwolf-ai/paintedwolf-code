//go:build linux

package exec

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func detachedDaemonCommand(t *testing.T, ctx context.Context, stay bool) (*exec.Cmd, func(), string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	testutil.FailErr(t, "locate daemon fixture runtime", err)
	path := filepath.Join(t.TempDir(), "daemon.pid")
	script := fmt.Sprintf(`import os, signal, time
path = %s
if os.fork() == 0:
    os.setsid()
    if os.fork() == 0:
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        with open(path, 'w') as file:
            file.write(str(os.getpid()))
        time.sleep(60)
    os._exit(0)
while not os.path.exists(path):
    time.sleep(0.01)
%s
`, strconv.Quote(path), map[bool]string{true: "time.sleep(60)", false: ""}[stay])
	cmd, cleanup, err := PrepareCommand(ctx, python, []string{"-c", script}, ExecOpts{Launch: HostLaunch("detached daemon test")})
	testutil.FailErr(t, "prepare detached command", err)
	return cmd, cleanup, path
}

func supervisedDaemonCommand(t *testing.T) (*exec.Cmd, func(), string) {
	t.Helper()
	cmd, cleanup, path := detachedDaemonCommand(t, context.Background(), true)
	cmd, cleanup, err := superviseCommand(cmd, cleanup)
	testutil.FailErr(t, "supervise daemon fixture", err)
	return cmd, cleanup, path
}

func TestSupervisorCleansDoubleForkedNewSessionOnCommandCompletion(t *testing.T) {
	cmd, cleanup, path := detachedDaemonCommand(t, context.Background(), false)
	defer cleanup()
	testutil.FailErr(t, "run daemon launcher", RunInOwnGroup(cmd))
	pid := waitForPIDFile(t, path)
	testutil.FailErr(t, "detached daemon survived completion", waitForProcessGone(pid, 5*time.Second))
}

func TestSupervisorCancellationSparesAnotherCommandsDetachedDaemon(t *testing.T) {
	other, otherCleanup, otherPath := supervisedDaemonCommand(t)
	defer otherCleanup()
	testutil.FailErr(t, "start independent supervisor", other.Start())
	otherPID := waitForPIDFile(t, otherPath)
	defer func() { otherCleanup(); _ = other.Wait() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, cleanup, path := detachedDaemonCommand(t, ctx, true)
	defer cleanup()
	done := make(chan error, 1)
	go func() { done <- RunInOwnGroup(cmd) }()
	pid := waitForPIDFile(t, path)
	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("cancelled supervisor did not settle")
	}
	testutil.FailErr(t, "cancelled detached daemon survived", waitForProcessGone(pid, 5*time.Second))
	if err := waitForProcessGone(otherPID, 100*time.Millisecond); err == nil {
		t.Fatal("cancellation killed a different command's daemon")
	}
}

func TestSupervisorEngineLifetimeEOFRemovesDetachedDaemon(t *testing.T) {
	cmd, cleanup, path := supervisedDaemonCommand(t)
	defer cleanup()
	testutil.FailErr(t, "start supervisor", cmd.Start())
	pid := waitForPIDFile(t, path)
	cleanup()
	_ = cmd.Wait()
	testutil.FailErr(t, "daemon survived engine lifetime EOF", waitForProcessGone(pid, 5*time.Second))
}

func TestSupervisorCompanionRequestsOwnedTreeCleanup(t *testing.T) {
	cmd, cleanup, path := supervisedDaemonCommand(t)
	defer cleanup()
	testutil.FailErr(t, "start supervised command", cmd.Start())
	pid := waitForPIDFile(t, path)
	reapLines(fmt.Sprintf("+s%d\n", cmd.Process.Pid))
	_ = cmd.Wait()
	testutil.FailErr(t, "daemon survived companion cleanup", waitForProcessGone(pid, 5*time.Second))
}
