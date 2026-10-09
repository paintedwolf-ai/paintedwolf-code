//go:build unix

package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

const (
	drainInvariantRoleEnv     = "PW_TEST_DRAIN_INVARIANT_ROLE"
	drainInvariantWorkloadEnv = "PW_TEST_DRAIN_INVARIANT_WORKLOAD"
	drainInvariantDirEnv      = "PW_TEST_DRAIN_INVARIANT_DIR"
	drainInvariantExitEnv     = "PW_TEST_DRAIN_INVARIANT_EXIT"
	drainInvariantDetachEnv   = "PW_TEST_DRAIN_INVARIANT_DETACH"

	workloadSlowBytes = "slow_bytes"
	workloadDevZero   = "dev_zero"

	exitModeNormal     = "normal"
	exitModeWaitCancel = "wait_cancel"

	// Allow scheduler overhead around the drain ceiling; supervised trees may settle sooner.
	maxDrainCeiling = PipelineWaitDelay + 5*time.Second
)

type invariantTrackingWriter struct {
	mu           sync.Mutex
	buf          bytes.Buffer
	bytesWritten atomic.Int64
}

func (w *invariantTrackingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.bytesWritten.Add(int64(len(p)))
	if w.buf.Len() < 64*1024 {
		limit := 64*1024 - w.buf.Len()
		if len(p) < limit {
			w.buf.Write(p)
		} else {
			w.buf.Write(p[:limit])
		}
	}
	return len(p), nil
}

func assertProcessGroupReaped(t *testing.T, pgid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	err := syscall.Kill(-pgid, 0)
	if !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process group %d not reaped within %v: syscall.Kill(-pgid, 0) = %v, want ESRCH", pgid, within, err)
	}
}

func waitForPIDFileHelper(path string) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runGrandchildWorkload(workload string) {
	switch workload {
	case workloadDevZero:
		buf := make([]byte, 16384)
		for {
			if _, err := os.Stdout.Write(buf); err != nil {
				return
			}
		}
	default:
		payload := []byte("adversarial slow byte drain payload\n")
		for {
			if _, err := os.Stdout.Write(payload); err != nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
}

// TestPipelineDrainInvariantHelperProcess implements the adversarial descendant
// tree when re-exec'd with drainInvariantRoleEnv set.
func TestPipelineDrainInvariantHelperProcess(t *testing.T) {
	role := os.Getenv(drainInvariantRoleEnv)
	if role == "" {
		t.Skip("helper process for pipeline drain invariant tests")
	}
	dir := os.Getenv(drainInvariantDirEnv)
	workload := os.Getenv(drainInvariantWorkloadEnv)
	exitMode := os.Getenv(drainInvariantExitEnv)
	detach := os.Getenv(drainInvariantDetachEnv) == "1"

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "locate self: %v\n", err)
		os.Exit(1)
	}

	switch role {
	case "leader":
		_ = os.WriteFile(filepath.Join(dir, "leader.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
		child := osexec.Command(self, "-test.run=^TestPipelineDrainInvariantHelperProcess$", "-test.v=false")
		child.Env = append(os.Environ(), drainInvariantRoleEnv+"=child")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "start child: %v\n", err)
			os.Exit(1)
		}
		waitForPIDFileHelper(filepath.Join(dir, "grandchild.pid"))
		if exitMode == exitModeWaitCancel {
			time.Sleep(2 * time.Minute)
		}
		os.Exit(0)

	case "child":
		_ = os.WriteFile(filepath.Join(dir, "child.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
		grandchild := osexec.Command(self, "-test.run=^TestPipelineDrainInvariantHelperProcess$", "-test.v=false")
		grandchild.Env = append(os.Environ(), drainInvariantRoleEnv+"=grandchild")
		grandchild.Stdout = os.Stdout
		grandchild.Stderr = os.Stderr
		if detach {
			grandchild.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		}
		if err := grandchild.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "start grandchild: %v\n", err)
			os.Exit(1)
		}
		waitForPIDFileHelper(filepath.Join(dir, "grandchild.pid"))
		if exitMode == exitModeWaitCancel && !detach {
			time.Sleep(2 * time.Minute)
		}
		os.Exit(0)

	case "grandchild":
		// Publish readiness only after output reaches the inherited pipe.
		_, _ = os.Stdout.Write([]byte("grandchild ready\n"))
		_ = os.WriteFile(filepath.Join(dir, "grandchild.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
		runGrandchildWorkload(workload)
		os.Exit(0)

	default:
		os.Exit(1)
	}
}

func setupDescendantTree(t *testing.T, workload, exitMode string, detach bool) (string, []string, map[string]string, string) {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)

	env := map[string]string{
		drainInvariantRoleEnv:     "leader",
		drainInvariantWorkloadEnv: workload,
		drainInvariantDirEnv:      dir,
		drainInvariantExitEnv:     exitMode,
	}
	if detach {
		env[drainInvariantDetachEnv] = "1"
	}

	args := []string{"-test.run=^TestPipelineDrainInvariantHelperProcess$", "-test.v=false"}
	return self, args, env, dir
}

// TestPipelineDrainInvariant_NormalExit verifies that an adversarial descendant
// tree holding output pipes open via slow bytes or /dev/zero is bounded by the
// 5s drain ceiling, reaps the process group cleanly with no zombies, and unblocks
// reader goroutines.
func TestPipelineDrainInvariant_NormalExit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		workload string
	}{
		{name: "slow_bytes", workload: workloadSlowBytes},
		{name: "dev_zero", workload: workloadDevZero},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary, args, env, dir := setupDescendantTree(t, tc.workload, exitModeNormal, false)

			tw := &invariantTrackingWriter{}
			opts := ExecOpts{
				Launch:    HostLaunch("drain invariant test"),
				InlineEnv: env,
				Stdout:    tw,
				Timeout:   15 * time.Second,
			}

			started := time.Now()
			result, err := RunPipeline(context.Background(), []Stage{{Name: binary, Args: args}}, opts)
			elapsed := time.Since(started)

			if err != nil {
				t.Fatalf("RunPipeline failed: %v", err)
			}
			if result.ExitCode != 0 {
				t.Fatalf("result exit code = %d, want 0", result.ExitCode)
			}

			// Execution stays bounded even when descendants hold pipes open.
			if elapsed > maxDrainCeiling {
				t.Fatalf("execution finished in %v, want within %v of the drain ceiling",
					elapsed, maxDrainCeiling)
			}

			// Invariant 2: Process group reaping and zombie-free lifecycle.
			leaderPid := waitForPIDFile(t, filepath.Join(dir, "leader.pid"))
			childPid := waitForPIDFile(t, filepath.Join(dir, "child.pid"))
			grandchildPid := waitForPIDFile(t, filepath.Join(dir, "grandchild.pid"))
			pgid := leaderPid

			assertProcessGroupReaped(t, pgid, 5*time.Second)
			for _, pid := range []int{leaderPid, childPid, grandchildPid} {
				if err := waitForProcessGone(pid, 5*time.Second); err != nil {
					t.Fatalf("lingering process %d: %v", pid, err)
				}
			}

			// Invariant 3: Pipe handle closure and reader goroutine unblocking.
			if tw.bytesWritten.Load() == 0 {
				t.Fatal("expected reader goroutine to receive bytes before pipe closure")
			}
		})
	}
}

// TestPipelineDrainInvariant_ForegroundCancellation_SameProcessGroup verifies
// that foreground cancellation reaps the full in-group adversarial descendant tree,
// closes output pipes, and unblocks reader goroutines without lingering zombies.
func TestPipelineDrainInvariant_ForegroundCancellation_SameProcessGroup(t *testing.T) {
	binary, args, env, dir := setupDescendantTree(t, workloadSlowBytes, exitModeWaitCancel, false)

	tw := &invariantTrackingWriter{}
	opts := ExecOpts{
		Launch:    HostLaunch("drain invariant cancel test"),
		InlineEnv: env,
		Stdout:    tw,
		Timeout:   15 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var (
		res *PipelineResult
		err error
	)
	go func() {
		defer close(done)
		res, err = RunPipeline(ctx, []Stage{{Name: binary, Args: args}}, opts)
	}()

	leaderPid := waitForPIDFile(t, filepath.Join(dir, "leader.pid"))
	childPid := waitForPIDFile(t, filepath.Join(dir, "child.pid"))
	grandchildPid := waitForPIDFile(t, filepath.Join(dir, "grandchild.pid"))
	pgid := leaderPid

	// Cancel once the entire descendant tree is established and writing.
	cancel()

	select {
	case <-done:
	case <-time.After(TerminateGrace + 5*time.Second):
		t.Fatal("cancelled pipeline did not settle within grace period")
	}

	_ = res
	_ = err

	// Invariant: Entire process group reaped and no lingering zombies.
	assertProcessGroupReaped(t, pgid, 5*time.Second)
	for _, pid := range []int{leaderPid, childPid, grandchildPid} {
		if err := waitForProcessGone(pid, 5*time.Second); err != nil {
			t.Fatalf("lingering process %d after cancellation: %v", pid, err)
		}
	}

	// Invariant: Pipe handles closed and reader unblocked cleanly.
	if tw.bytesWritten.Load() == 0 {
		t.Fatal("expected output received before cancellation")
	}
}

// TestPipelineDrainInvariant_ForegroundCancellation_DetachedDrainCeiling verifies
// that when a detached descendant holds output pipes open past leader cancellation,
// execution settles within the drain ceiling plus scheduler overhead.
func TestPipelineDrainInvariant_ForegroundCancellation_DetachedDrainCeiling(t *testing.T) {
	binary, args, env, dir := setupDescendantTree(t, workloadSlowBytes, exitModeWaitCancel, true)

	tw := &invariantTrackingWriter{}
	opts := ExecOpts{
		Launch:    HostLaunch("drain invariant detached cancel test"),
		InlineEnv: env,
		Stdout:    tw,
		Timeout:   15 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = RunPipeline(ctx, []Stage{{Name: binary, Args: args}}, opts)
	}()

	leaderPid := waitForPIDFile(t, filepath.Join(dir, "leader.pid"))
	grandchildPid := waitForPIDFile(t, filepath.Join(dir, "grandchild.pid"))
	t.Cleanup(func() {
		_ = syscall.Kill(grandchildPid, syscall.SIGKILL)
	})

	// Cancel leader; detached grandchild continues holding pipes open.
	cancelledAt := time.Now()
	cancel()

	select {
	case <-done:
	case <-time.After(maxDrainCeiling + 5*time.Second):
		t.Fatal("cancelled pipeline with detached holder did not settle")
	}
	elapsed := time.Since(cancelledAt)

	// Cancellation stays bounded whether supervision closes pipes or drain expires.
	if elapsed > maxDrainCeiling {
		t.Fatalf("cancellation settled in %v, want within %v of the drain ceiling",
			elapsed, maxDrainCeiling)
	}

	// Invariant: Leader reaped.
	if err := waitForProcessGone(leaderPid, 5*time.Second); err != nil {
		t.Fatalf("leader %d lingered after cancel: %v", leaderPid, err)
	}

	// Invariant: Pipe closed unblocking reader.
	if tw.bytesWritten.Load() == 0 {
		t.Fatal("expected output received before pipe closure")
	}
}

// TestPipelineDrainInvariant_AsyncPipelineDrainCeiling verifies that StartPipelineAsync
// enforces the 5s drain ceiling, cleanly closes pipes, and reaps the process group.
func TestPipelineDrainInvariant_AsyncPipelineDrainCeiling(t *testing.T) {
	binary, args, env, dir := setupDescendantTree(t, workloadSlowBytes, exitModeNormal, false)

	tw := &invariantTrackingWriter{}
	opts := ExecOpts{
		Launch:    HostLaunch("async drain invariant test"),
		InlineEnv: env,
	}

	started := time.Now()
	async, err := StartPipelineAsync(context.Background(), []Stage{{Name: binary, Args: args}}, opts, tw, io.Discard)
	testutil.FailErr(t, "start async pipeline", err)
	t.Cleanup(async.Kill)

	select {
	case <-async.Done():
	case <-time.After(maxDrainCeiling + 5*time.Second):
		t.Fatal("async pipeline did not settle under drain ceiling")
	}
	elapsed := time.Since(started)

	result, err := async.Wait()
	testutil.FailErr(t, "settle async pipeline", err)
	if result.ExitCode != 0 {
		t.Fatalf("async result exit code = %d, want 0", result.ExitCode)
	}

	// Async completion stays bounded.
	if elapsed > maxDrainCeiling {
		t.Fatalf("async execution finished in %v, want within %v of the drain ceiling",
			elapsed, maxDrainCeiling)
	}

	// Invariant 2: Process group reaping.
	leaderPid := waitForPIDFile(t, filepath.Join(dir, "leader.pid"))
	pgid := leaderPid
	assertProcessGroupReaped(t, pgid, 5*time.Second)

	// Invariant 3: Reader unblocked cleanly.
	if tw.bytesWritten.Load() == 0 {
		t.Fatal("expected async reader to receive bytes before pipe closure")
	}
}
