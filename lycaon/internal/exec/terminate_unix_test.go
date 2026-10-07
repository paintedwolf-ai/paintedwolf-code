//go:build unix

package exec

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// jobControlScript starts a job the shell moves into its own process group,
// the way an interactive shell runs every command, and waits on it.
func jobControlScript(pidFile string) string {
	return fmt.Sprintf("set -m; sleep 30 & echo $! > %s; wait", strconv.Quote(pidFile))
}

func requireOwnGroup(t *testing.T, leader, job int) {
	t.Helper()
	pgid, err := syscall.Getpgid(job)
	testutil.FailErr(t, "job process group", err)
	if pgid == leader {
		t.Skip("this shell keeps monitor-mode jobs in its own process group")
	}
}

func TestUnixGuardTerminateEndsJobsOutsideTheLeadersGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "job.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard, err := newRunGuard(ProcessPriorityNormal)
	testutil.FailErr(t, "newRunGuard", err)
	cmd := exec.CommandContext(ctx, "sh", "-c", jobControlScript(pidFile))
	guard.configure(cmd)
	testutil.FailErr(t, "start leader", cmd.Start())
	testutil.FailErr(t, "guard onStarted", guard.onStarted(cmd))
	job := waitForPIDFile(t, pidFile)
	requireOwnGroup(t, cmd.Process.Pid, job)

	started := time.Now()
	guard.terminate(cmd)
	_ = cmd.Wait()
	guard.release()
	if elapsed := time.Since(started); elapsed > TerminateGrace {
		t.Fatalf("a tree that honours SIGTERM took %v, want under the %v kill grace", elapsed, TerminateGrace)
	}
	if err := waitForProcessGone(job, 5*time.Second); err != nil {
		_ = syscall.Kill(job, syscall.SIGKILL)
		testutil.FailErr(t, "job outside the leader's group outlived terminate", err)
	}
}

func TestUnixGuardTerminateKillsATreeIgnoringTerm(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "job.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard, err := newRunGuard(ProcessPriorityNormal)
	testutil.FailErr(t, "newRunGuard", err)
	// Ignored signals survive exec, so the sleep ignores SIGTERM too.
	cmd := exec.CommandContext(ctx, "sh", "-c",
		fmt.Sprintf("trap '' TERM; set -m; sleep 30 & echo $! > %s; wait", strconv.Quote(pidFile)))
	guard.configure(cmd)
	testutil.FailErr(t, "start leader", cmd.Start())
	testutil.FailErr(t, "guard onStarted", guard.onStarted(cmd))
	job := waitForPIDFile(t, pidFile)

	started := time.Now()
	guard.terminate(cmd)
	_ = cmd.Wait()
	guard.release()
	elapsed := time.Since(started)
	if elapsed < TerminateGrace/2 || elapsed > TerminateGrace+5*time.Second {
		t.Fatalf("kill fallback fired after %v, want about the %v grace", elapsed, TerminateGrace)
	}
	if err := waitForProcessGone(job, 5*time.Second); err != nil {
		_ = syscall.Kill(job, syscall.SIGKILL)
		testutil.FailErr(t, "job ignoring SIGTERM outlived the kill fallback", err)
	}
}

// A job in its own group inherits the pipeline's output pipe; the async wait
// has no delay of its own, so only ending that job lets Wait return.
func TestAsyncPipelineKillEndsJobsHoldingItsOutputPipe(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "job.pid")
	var stdout bytes.Buffer
	run, err := StartPipelineAsync(context.Background(),
		[]Stage{{Name: "sh", Args: []string{"-c", jobControlScript(pidFile)}}},
		ExecOpts{Launch: HostLaunch("exec test")}, &stdout, nil)
	testutil.FailErr(t, "start async", err)
	job := waitForPIDFile(t, pidFile)

	started := time.Now()
	run.Kill()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = run.Wait()
	}()
	select {
	case <-done:
	case <-time.After(TerminateGrace + 10*time.Second):
		_ = syscall.Kill(job, syscall.SIGKILL)
		t.Fatal("async pipeline never settled after Kill")
	}
	if elapsed := time.Since(started); elapsed > TerminateGrace+5*time.Second {
		t.Fatalf("settling took %v", elapsed)
	}
	if err := waitForProcessGone(job, 5*time.Second); err != nil {
		_ = syscall.Kill(job, syscall.SIGKILL)
		testutil.FailErr(t, "job holding the output pipe outlived Kill", err)
	}
}
