//go:build linux

package exec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// supervisorLaunchGateEnv names a file the re-executed supervisor waits for
// before it installs its signal handlers.
const supervisorLaunchGateEnv = "PW_TEST_SUPERVISOR_LAUNCH_GATE"

// Package variables initialise before the supervisor's init re-enters it.
var _ = holdSupervisorLaunch(os.Getenv(supervisorLaunchGateEnv))

func holdSupervisorLaunch(gate string) bool {
	if gate == "" {
		return false
	}
	beforeSupervisorSignals = func() {
		for {
			if _, err := os.Stat(gate); err == nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
	return true
}

func TestTerminationBeforeSupervisorLaunchReportStopsLaunchedCommand(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate")
	cmd, cleanup, err := PrepareCommand(context.Background(), "sleep", []string{"30"}, ExecOpts{Launch: HostLaunch("supervisor launch test")})
	testutil.FailErr(t, "prepare command", err)
	cmd, cleanup, err = superviseCommand(cmd, cleanup, ProcessPriorityNormal)
	testutil.FailErr(t, "supervise command", err)
	defer cleanup()
	cmd.Env = append(cmd.Env, supervisorLaunchGateEnv+"="+gate)
	guard, err := newRunGuard(ProcessPriorityNormal)
	testutil.FailErr(t, "create guard", err)
	defer guard.release()
	guard.configure(cmd)
	testutil.FailErr(t, "start supervisor", cmd.Start())
	// The deadline elapses while the supervisor is still starting.
	guard.terminate(cmd)
	testutil.FailErr(t, "open supervisor gate", os.WriteFile(gate, nil, 0o600))
	if err := guard.onStarted(cmd); err != nil {
		guard.kill(cmd)
		_ = cmd.Wait()
		t.Fatalf("termination during startup failed the launch: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err = <-waited:
	case <-time.After(10 * time.Second):
		guard.kill(cmd)
		t.Fatal("held termination did not stop the launched command")
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("wait error = %v want a signalled exit", err)
	}
	if status, ok := exit.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("wait status = %v want terminated by SIGTERM", exit)
	}
}
