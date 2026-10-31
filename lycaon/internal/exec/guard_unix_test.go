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

	// Reap the leader through the guard before killing.
	_ = guard.wait(cmd)

	guard.kill(cmd)

	if err := waitForProcessGone(t.Context(), grandchild); err != nil {
		_ = syscall.Kill(grandchild, syscall.SIGKILL)
		testutil.FailErr(t, "descendant outlived guard kill", err)
	}
}

func TestReapedSessionReuseNeverSignalsStandInProcess(t *testing.T) {
	bystander := exec.Command("cat")
	stdin, err := bystander.StdinPipe()
	testutil.FailErr(t, "bystander stdin", err)
	defer func() { _ = stdin.Close() }()
	testutil.FailErr(t, "start bystander", bystander.Start())
	defer func() {
		if bystander.ProcessState == nil {
			_ = bystander.Process.Kill()
			_ = bystander.Wait()
		}
	}()
	bystanderInc, ok := osprocess.IncarnationOf(bystander.Process.Pid)
	if !ok {
		t.Fatal("failed to read bystander incarnation")
	}

	t.Run("release and fallback timer", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rawGuard, err := newRunGuard(ProcessPriorityNormal)
		testutil.FailErr(t, "newRunGuard", err)
		g := rawGuard.(*unixGuard)

		cmd := exec.CommandContext(ctx, "true")
		g.configure(cmd)
		testutil.FailErr(t, "start leader", cmd.Start())
		testutil.FailErr(t, "onStarted", g.onStarted(cmd))
		testutil.FailErr(t, "guard wait", g.wait(cmd))

		// Simulate the emptied session ID being reused by bystander after reap.
		var groupSignals []osprocess.Incarnation
		g.ops.sessionMembers = func(int) []int { return []int{bystander.Process.Pid} }
		g.ops.signalGroup = func(leader osprocess.Incarnation, sig syscall.Signal) error {
			groupSignals = append(groupSignals, leader)
			return osprocess.SignalGroup(leader, sig)
		}

		// Simulate the TerminateGrace fallback timer firing after reap, then release.
		g.kill(cmd)
		g.release()

		if len(groupSignals) != 0 {
			t.Fatalf("post-reap guard signalled group %+v", groupSignals)
		}
		if !bystanderInc.Alive() {
			t.Fatal("stand-in process in reused session was killed by post-reap guard")
		}
	})

	t.Run("RunInOwnGroup", func(t *testing.T) {
		cmd, cleanup, err := PrepareCommand(context.Background(), "true", nil, ExecOpts{Launch: HostLaunch("exec test")})
		testutil.FailErr(t, "PrepareCommand", err)
		defer cleanup()
		testutil.FailErr(t, "RunInOwnGroup", RunInOwnGroup(cmd))
		if !bystanderInc.Alive() {
			t.Fatal("bystander died after RunInOwnGroup")
		}
	})

	t.Run("terminateWired after first stage reaped", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		rawGuard0, err := newRunGuard(ProcessPriorityNormal)
		testutil.FailErr(t, "newRunGuard 0", err)
		g0 := rawGuard0.(*unixGuard)
		cmd0 := exec.CommandContext(ctx, "true")
		g0.configure(cmd0)
		testutil.FailErr(t, "start stage 0", cmd0.Start())
		testutil.FailErr(t, "onStarted stage 0", g0.onStarted(cmd0))

		rawGuard1, err := newRunGuard(ProcessPriorityNormal)
		testutil.FailErr(t, "newRunGuard 1", err)
		cmd1 := exec.CommandContext(ctx, "cat")
		stdin1, err := cmd1.StdinPipe()
		testutil.FailErr(t, "stage 1 stdin", err)
		defer func() { _ = stdin1.Close() }()
		rawGuard1.configure(cmd1)
		testutil.FailErr(t, "start stage 1", cmd1.Start())
		testutil.FailErr(t, "onStarted stage 1", rawGuard1.onStarted(cmd1))

		// Stage 0 finishes and is reaped while stage 1 is still running; its
		// session ID is then taken by bystander before terminateWired runs.
		stage0Reaped := make(chan struct{})
		origWait := g0.ops.awaitExit
		g0.ops.awaitExit = func(pid int) {
			origWait(pid)
			close(stage0Reaped)
		}
		g0.ops.sessionMembers = func(int) []int {
			select {
			case <-stage0Reaped:
				return []int{bystander.Process.Pid}
			default:
				return nil
			}
		}

		wired := []wiredStage{
			{cmd: cmd0, guard: g0, cleanup: func() {}},
			{cmd: cmd1, guard: rawGuard1, cleanup: func() {}},
		}
		defer releaseWired(wired)

		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = waitWiredStages(ctx, wired)
		}()

		<-stage0Reaped
		// Wait until stage 0 has marked itself reaped under g0.mu.
		testutil.WaitFor(t, func() bool {
			g0.mu.Lock()
			defer g0.mu.Unlock()
			return g0.reaped
		})
		cancel()
		_ = stdin1.Close()
		<-done

		if !bystanderInc.Alive() {
			t.Fatal("terminateWired signalled stand-in process in stage 0's reused session")
		}
	})
}

func TestMemberReplacedBetweenScanAndSignalReceivesNoSignal(t *testing.T) {
	bystander := exec.Command("cat")
	stdin, err := bystander.StdinPipe()
	testutil.FailErr(t, "bystander stdin", err)
	defer func() { _ = stdin.Close() }()
	testutil.FailErr(t, "start bystander", bystander.Start())
	defer func() {
		if bystander.ProcessState == nil {
			_ = bystander.Process.Kill()
			_ = bystander.Wait()
		}
	}()
	bystanderInc, ok := osprocess.IncarnationOf(bystander.Process.Pid)
	if !ok {
		t.Fatal("failed to read bystander incarnation")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rawGuard, err := newRunGuard(ProcessPriorityNormal)
	testutil.FailErr(t, "newRunGuard", err)
	g := rawGuard.(*unixGuard)
	defer g.release()

	cmd := exec.CommandContext(ctx, "true")
	g.configure(cmd)
	testutil.FailErr(t, "start leader", cmd.Start())
	testutil.FailErr(t, "onStarted", g.onStarted(cmd))

	// During the pre-reap snapshot, the member PID was observed with an earlier
	// start timestamp; before signal delivery it exited and bystander took its PID.
	g.ops.sessionMembers = func(int) []int { return []int{bystander.Process.Pid} }
	g.ops.incarnationOf = func(pid int) (osprocess.Incarnation, bool) {
		if pid == bystander.Process.Pid {
			return osprocess.Incarnation{PID: pid, Start: bystanderInc.Start - 1}, true
		}
		return osprocess.IncarnationOf(pid)
	}

	testutil.FailErr(t, "guard wait", g.wait(cmd))
	g.kill(cmd)

	if !bystanderInc.Alive() {
		t.Fatal("bystander with reassigned PID and newer start time was signalled")
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
	case <-t.Context().Done():
		_ = syscall.Kill(grandchild, syscall.SIGKILL)
		t.Fatal("RunPipeline did not return after its timeout")
	}
	if err := waitForProcessGone(t.Context(), grandchild); err != nil {
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

	const waitDelay = 50 * time.Millisecond
	done := make(chan struct{})
	var (
		res    *PipelineResult
		runErr error
	)
	go func() {
		defer close(done)
		res, runErr = runPipeline(context.Background(), []Stage{{Name: "sh", Args: []string{"-c", script}}}, ExecOpts{Launch: HostLaunch("exec test"),
			Dir:       dir,
			Timeout:   2 * time.Second,
			InlineEnv: map[string]string{detachHelperEnv: pidFile},
		}, waitDelay)
	}()

	detached := waitForPIDFile(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(detached, syscall.SIGKILL) })

	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("RunPipeline blocked on a descendant that left the process group")
	}
	if runErr != nil || res == nil || res.TimedOut {
		t.Fatalf("RunPipeline res=%+v err=%v, want waitDelay pipe release before 2s command timeout", res, runErr)
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
	for t.Context().Err() == nil {
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

func waitForProcessGone(ctx context.Context, pid int) error {
	for ctx.Err() == nil {
		if !osprocess.Alive(pid) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("process %d still running: %w", pid, ctx.Err())
}
