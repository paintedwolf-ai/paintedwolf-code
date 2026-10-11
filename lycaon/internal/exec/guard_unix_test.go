//go:build unix

package exec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/testutil"
)

// detachHelperEnv starts a descendant in a separate process group.
const detachHelperEnv = "PW_TEST_DETACH_HELPER"

func TestUnixGuardKillsGroupAfterLeaderIsReaped(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	guard, err := newRunGuard(ProcessPriorityNormal)
	testutil.FailErr(t, "newRunGuard", err)
	defer guard.release()

	// The descendant remains alive after its leader exits.
	script := fmt.Sprintf("sh -c 'echo $$ > %s; sleep 30' & exit 0", pidFile)
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	guard.configure(cmd)
	testutil.FailErr(t, "start leader", cmd.Start())
	testutil.FailErr(t, "guard onStarted", guard.onStarted(cmd))

	grandchild := waitForPIDFile(t, pidFile)

	// Reap the leader before killing. Getpgid cannot answer for a reaped pid, so
	// a guard that looks the group up here has nothing left to signal.
	_ = cmd.Wait()

	guard.kill(cmd)

	if err := waitForProcessGone(grandchild, 5*time.Second); err != nil {
		_ = syscall.Kill(grandchild, syscall.SIGKILL)
		testutil.FailErr(t, "descendant outlived guard kill", err)
	}
}

func TestRunPipelineTimeoutKillsDescendants(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")

	script := fmt.Sprintf("sh -c 'echo $$ > %s; sleep 30' & sleep 30", pidFile)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = RunPipeline(context.Background(), []Stage{{Name: "sh", Args: []string{"-c", script}}},
			ExecOpts{Launch: HostLaunch("exec test"), Dir: dir, Timeout: 500 * time.Millisecond})
	}()

	grandchild := waitForPIDFile(t, pidFile)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = syscall.Kill(grandchild, syscall.SIGKILL)
		t.Fatal("RunPipeline did not return after its timeout")
	}
	if err := waitForProcessGone(grandchild, 5*time.Second); err != nil {
		_ = syscall.Kill(grandchild, syscall.SIGKILL)
		testutil.FailErr(t, "descendant outlived pipeline timeout", err)
	}
}

// An escaped descendant cannot hold the caller past the wait deadline.
func TestRunPipelineReturnsWhenDescendantEscapesProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "detached.pid")

	self, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)
	// The detached helper inherits the pipeline's stdout, which is what holds
	// Wait open once the leader is gone.
	script := fmt.Sprintf("%s -test.run=TestExecDetachHelperProcess -test.v=false & while [ ! -s %s ]; do sleep 0.01; done; exit 0",
		strconv.Quote(self), strconv.Quote(pidFile))

	started := time.Now()
	const waitDelay = 50 * time.Millisecond
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runPipeline(context.Background(), []Stage{{Name: "sh", Args: []string{"-c", script}}}, ExecOpts{Launch: HostLaunch("exec test"),
			Dir:       dir,
			Timeout:   2 * time.Second,
			InlineEnv: map[string]string{detachHelperEnv: pidFile},
		}, waitDelay)
	}()

	detached := waitForPIDFile(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(detached, syscall.SIGKILL) })

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunPipeline blocked on a descendant that left the process group")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("RunPipeline returned after %v, want the wait bounded near %v", elapsed, waitDelay)
	}
}

// TestExecDetachHelperProcess is the detached descendant, not a test. It runs
// only when the parent case re-execs this binary with detachHelperEnv set.
func TestExecDetachHelperProcess(t *testing.T) {
	pidFile := strings.TrimSpace(os.Getenv(detachHelperEnv))
	if pidFile == "" {
		t.Skip("helper process for TestRunPipelineReturnsWhenDescendantEscapesProcessGroup")
	}
	if _, err := syscall.Setsid(); err != nil {
		fmt.Fprintf(os.Stderr, "setsid: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "write pid: %v\n", err)
		os.Exit(1)
	}
	// Hold inherited stdout past the deadline.
	// os.Exit avoids test-runner output in the captured stream.
	time.Sleep(90 * time.Second)
	os.Exit(0)
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("descendant never reported its pid at %s", path)
	return 0
}

func waitForProcessGone(pid int, within time.Duration) error {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !osprocess.Alive(pid) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("process %d still running after %v", pid, within)
}
