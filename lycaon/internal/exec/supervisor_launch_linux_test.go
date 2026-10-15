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

// gatedSupervisedSleep starts a supervised sleep held before its signal handlers
// until gate exists.
func gatedSupervisedSleep(t *testing.T, gate string, launchGrace time.Duration) (*exec.Cmd, *unixGuard) {
	t.Helper()
	cmd, cleanup, err := PrepareCommand(context.Background(), "sleep", []string{"30"}, ExecOpts{Launch: HostLaunch("supervisor launch test")})
	testutil.FailErr(t, "prepare command", err)
	cmd, cleanup, err = superviseCommand(cmd, cleanup, ProcessPriorityNormal)
	testutil.FailErr(t, "supervise command", err)
	t.Cleanup(cleanup)
	cmd.Env = append(cmd.Env, supervisorLaunchGateEnv+"="+gate)
	created, err := newRunGuard(ProcessPriorityNormal)
	testutil.FailErr(t, "create guard", err)
	guard := created.(*unixGuard)
	guard.launchGrace = launchGrace
	t.Cleanup(guard.release)
	guard.configure(cmd)
	testutil.FailErr(t, "start supervisor", cmd.Start())
	return cmd, guard
}

func waitSignalled(t *testing.T, cmd *exec.Cmd, guard *unixGuard, want syscall.Signal) {
	t.Helper()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var err error
	select {
	case err = <-waited:
	case <-time.After(10 * time.Second):
		guard.kill(cmd)
		t.Fatal("supervised command did not stop")
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("wait error = %v want a signalled exit", err)
	}
	if status, ok := exit.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != want {
		t.Fatalf("wait status = %v want %v", exit, want)
	}
}

func TestTerminationBeforeSupervisorLaunchReportStopsLaunchedCommand(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate")
	cmd, guard := gatedSupervisedSleep(t, gate, supervisorLaunchGrace)
	// The deadline elapses while the supervisor is still starting.
	guard.terminate(cmd)
	testutil.FailErr(t, "open supervisor gate", os.WriteFile(gate, nil, 0o600))
	if err := guard.onStarted(cmd); err != nil {
		guard.kill(cmd)
		_ = cmd.Wait()
		t.Fatalf("termination during startup failed the launch: %v", err)
	}
	waitSignalled(t, cmd, guard, syscall.SIGTERM)
}

func TestHeldTerminationKillsASupervisorThatNeverReportsLaunch(t *testing.T) {
	// The gate never opens, so the supervisor stays before its launch report.
	cmd, guard := gatedSupervisedSleep(t, filepath.Join(t.TempDir(), "gate"), 0)
	guard.terminate(cmd)
	if err := guard.onStarted(cmd); err == nil {
		t.Fatal("unreported launch was confirmed")
	}
	waitSignalled(t, cmd, guard, syscall.SIGKILL)
}
