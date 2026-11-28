package command

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// TestHoldWinsAdmissionHandshake covers the two-sided callback installation and
// stop delivery handshake across all three required orderings: stop before
// callback installation, callback installed before stop, and registration failure.
func TestHoldWinsAdmissionHandshake(t *testing.T) {
	t.Run("stop_before_setStop", func(t *testing.T) {
		completed := make(chan bgprocess.Completion, 2)
		registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
			Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
		})
		t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
		supervisor := &ProgramSupervisor{Background: registry}
		jobs := supervisor.Jobs("sess-1", "proj-1")
		admitted, err := jobs.BeginProgram(t.Context(), ProgramJobOrigin{Tool: "command", ToolCallID: "call-1", RootSessionID: "sess-1"})
		testutil.FailErr(t, "begin program", err)

		paused := make(chan string, 1)
		release := make(chan struct{})
		jobs.mu.Lock()
		jobs.beforeSetStop = func(handle string) {
			paused <- handle
			<-release
		}
		jobs.mu.Unlock()

		var bodyStarted atomic.Bool
		root := toolcontract.OperationInvocation{SessionID: "sess-1", Identity: toolcontract.ExecutionIdentity{ProgramID: "prog-handshake-1", ExecutionID: "root"}}
		runDone := make(chan error, 1)
		go func() {
			_, runErr := supervisor.RunProgram(admitted, root, func(runCtx context.Context) (toolcontract.ProducerResult, error) {
				_, startErr := supervisor.StartJob(runCtx, root, "held-job", func(jobCtx context.Context, _ string, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
					bodyStarted.Store(true)
					return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, nil
				})
				return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, startErr
			})
			runDone <- runErr
		}()

		handle := testutil.Receive(t, "paused before setStop", paused)
		supervisor.StopProgram("prog-handshake-1", bgprocess.TerminationStoppedByUser)
		close(release)

		<-runDone
		c := testutil.Receive(t, "job completion", completed)
		if c.Handle != handle {
			t.Fatalf("completed handle=%q, want %q", c.Handle, handle)
		}
		if c.TerminationReason != bgprocess.TerminationStoppedByUser {
			t.Fatalf("job termination reason = %q, want stopped_by_user", c.TerminationReason)
		}
		if bodyStarted.Load() {
			t.Fatal("job body started new work despite pending stop")
		}
		testutil.WaitFor(t, "registry capacity returns to baseline", func() bool {
			return len(registry.ActiveJobs("sess-1")) == 0
		})
		supervisor.mu.Lock()
		remainingHolds := 0
		if prog := supervisor.programs["prog-handshake-1"]; prog != nil {
			remainingHolds = len(prog.jobs)
		}
		supervisor.mu.Unlock()
		if remainingHolds != 0 {
			t.Fatalf("remaining holds = %d, want 0", remainingHolds)
		}
	})

	t.Run("setStop_before_stop", func(t *testing.T) {
		completed := make(chan bgprocess.Completion, 2)
		registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
			Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
		})
		t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
		supervisor := &ProgramSupervisor{Background: registry}
		jobs := supervisor.Jobs("sess-1", "proj-1")
		admitted, err := jobs.BeginProgram(t.Context(), ProgramJobOrigin{Tool: "command", ToolCallID: "call-2", RootSessionID: "sess-1"})
		testutil.FailErr(t, "begin program", err)

		entered := make(chan struct{})
		root := toolcontract.OperationInvocation{SessionID: "sess-1", Identity: toolcontract.ExecutionIdentity{ProgramID: "prog-handshake-2", ExecutionID: "root"}}
		runDone := make(chan error, 1)
		var causeReason atomic.Value
		go func() {
			_, runErr := supervisor.RunProgram(admitted, root, func(runCtx context.Context) (toolcontract.ProducerResult, error) {
				_, startErr := supervisor.StartJob(runCtx, root, "held-job", func(jobCtx context.Context, _ string, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
					noteJobNative(jobCtx, []string{"held-job"})
					close(entered)
					<-jobCtx.Done()
					causeReason.Store(bgprocess.TerminationReasonFromContext(jobCtx, bgprocess.TerminationStoppedByAgent))
					return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted}, jobCtx.Err()
				})
				if startErr != nil {
					return toolcontract.ProducerResult{}, startErr
				}
				<-runCtx.Done()
				return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted}, runCtx.Err()
			})
			runDone <- runErr
		}()

		testutil.AwaitClosed(t, "held job running", entered)
		supervisor.StopProgram("prog-handshake-2", bgprocess.TerminationStoppedByUser)
		<-runDone

		c := testutil.Receive(t, "job completion", completed)
		if c.TerminationReason != bgprocess.TerminationStoppedByUser {
			t.Fatalf("job termination reason = %q, want stopped_by_user", c.TerminationReason)
		}
		if got := causeReason.Load(); got != bgprocess.TerminationStoppedByUser {
			t.Fatalf("job context reason = %v, want stopped_by_user", got)
		}
		testutil.WaitFor(t, "registry capacity returns to baseline", func() bool {
			return len(registry.ActiveJobs("sess-1")) == 0
		})
	})

	t.Run("registration_failure_releases_hold_and_capacity", func(t *testing.T) {
		registry := bgprocess.NewRegistry(bgprocess.Config{MaxBackground: 1}, bgprocess.Hooks{})
		t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
		testutil.FailErr(t, "close registry", registry.Lifecycle.Close(t.Context()))
		supervisor := &ProgramSupervisor{Background: registry}
		jobs := supervisor.Jobs("sess-1", "proj-1")
		admitted, err := jobs.BeginProgram(t.Context(), ProgramJobOrigin{Tool: "command", ToolCallID: "call-3", RootSessionID: "sess-1"})
		testutil.FailErr(t, "begin program", err)

		root := toolcontract.OperationInvocation{SessionID: "sess-1", Identity: toolcontract.ExecutionIdentity{ProgramID: "prog-reg-fail", ExecutionID: "root"}}
		_, err = supervisor.RunProgram(admitted, root, func(runCtx context.Context) (toolcontract.ProducerResult, error) {
			_, startErr := supervisor.StartJob(runCtx, root, "will-fail", func(context.Context, string, commandprogram.Streams) (toolcontract.ProducerResult, error) {
				t.Error("body ran despite registration failure")
				return toolcontract.ProducerResult{}, nil
			})
			if !errors.Is(startErr, bgprocess.ErrRegistryClosed) {
				t.Errorf("startErr = %v, want ErrRegistryClosed", startErr)
			}
			supervisor.mu.Lock()
			holds := len(supervisor.programs["prog-reg-fail"].jobs)
			supervisor.mu.Unlock()
			if holds != 0 {
				t.Errorf("holds after registration failure = %d, want 0", holds)
			}
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, nil
		})
		testutil.FailErr(t, "run program", err)
		if len(registry.ActiveJobs("sess-1")) != 0 {
			t.Fatal("registration failure left active jobs")
		}
	})
}

// TestStopProgramAfterForegroundDoneClosesFurtherHoldsAndSettlesStopped verifies
// closure after foreground completion while a held job still keeps the program alive.
func TestStopProgramAfterForegroundDoneClosesFurtherHoldsAndSettlesStopped(t *testing.T) {
	completed := make(chan bgprocess.Completion, 2)
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
	})
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
	supervisor := &ProgramSupervisor{Background: registry}
	jobs := supervisor.Jobs("sess-1", "proj-1")
	admitted, err := jobs.BeginProgram(t.Context(), ProgramJobOrigin{Tool: "command", ToolCallID: "call-fg-done", RootSessionID: "sess-1"})
	testutil.FailErr(t, "begin program", err)

	var retainedCtx context.Context
	jobStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var releaseOnce sync.Once
	unlockCleanup := func() { releaseOnce.Do(func() { close(releaseCleanup) }) }
	t.Cleanup(unlockCleanup)

	root := toolcontract.OperationInvocation{SessionID: "sess-1", Identity: toolcontract.ExecutionIdentity{ProgramID: "prog-fg-done", ExecutionID: "root"}}
	_, err = supervisor.RunProgram(admitted, root, func(runCtx context.Context) (toolcontract.ProducerResult, error) {
		retainedCtx = runCtx
		_, startErr := supervisor.StartJob(runCtx, root, "held-after-fg", func(jobCtx context.Context, _ string, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
			noteJobNative(jobCtx, []string{"held-after-fg"})
			close(jobStarted)
			<-jobCtx.Done()
			<-releaseCleanup
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted}, jobCtx.Err()
		})
		return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, startErr
	})
	testutil.FailErr(t, "foreground completed", err)
	testutil.AwaitClosed(t, "held job running", jobStarted)

	settled := make(chan bool, 1)
	supervisor.AfterJobs("prog-fg-done", func(stopped bool) { settled <- stopped })

	// Stop the retained program after foreground completion.
	supervisor.StopProgram("prog-fg-done", bgprocess.TerminationStoppedByUser)

	// Further Hold and AdmitChild for the same program must immediately fail.
	if _, _, _, holdErr := supervisor.Hold(retainedCtx); holdErr == nil {
		t.Fatal("Hold succeeded after StopProgram on foreground-done program")
	}
	if admitErr := supervisor.AdmitChild(retainedCtx); admitErr == nil {
		t.Fatal("AdmitChild succeeded after StopProgram on foreground-done program")
	}

	unlockCleanup()
	if !testutil.Receive(t, "AfterJobs settlement", settled) {
		t.Fatal("AfterJobs reported stopped=false after StopProgram")
	}
	c := testutil.Receive(t, "held job completion", completed)
	if c.TerminationReason != bgprocess.TerminationStoppedByUser {
		t.Fatalf("held job reason = %q, want stopped_by_user", c.TerminationReason)
	}
}

// TestDisposeSessionPreservesChatDeletedReasonOnHeldJobs verifies disposal paths
// carry their own reason rather than overwriting with stopped_by_agent.
func TestDisposeSessionPreservesChatDeletedReasonOnHeldJobs(t *testing.T) {
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
	supervisor := &ProgramSupervisor{Background: registry}
	jobs := supervisor.Jobs("sess-dispose", "proj-1")
	admitted, err := jobs.BeginProgram(t.Context(), ProgramJobOrigin{Tool: "command", ToolCallID: "call-disp", RootSessionID: "sess-dispose"})
	testutil.FailErr(t, "begin program", err)

	started := make(chan struct{})
	var gotReason atomic.Value
	root := toolcontract.OperationInvocation{SessionID: "sess-dispose", Identity: toolcontract.ExecutionIdentity{ProgramID: "prog-disp", ExecutionID: "root"}}
	_, err = supervisor.RunProgram(admitted, root, func(runCtx context.Context) (toolcontract.ProducerResult, error) {
		_, startErr := supervisor.StartJob(runCtx, root, "held-dispose", func(jobCtx context.Context, _ string, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
			noteJobNative(jobCtx, []string{"held-dispose"})
			close(started)
			<-jobCtx.Done()
			gotReason.Store(bgprocess.TerminationReasonFromContext(jobCtx, bgprocess.TerminationStoppedByAgent))
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted}, jobCtx.Err()
		})
		return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, startErr
	})
	testutil.FailErr(t, "run program", err)
	testutil.AwaitClosed(t, "held job started", started)

	testutil.FailErr(t, "dispose session", supervisor.DisposeSession(t.Context(), "sess-dispose"))
	if got := gotReason.Load(); got != bgprocess.TerminationChatDeleted {
		t.Fatalf("disposed job reason = %v, want chat_deleted", got)
	}
}

// TestProgramExplicitStopOverlappingSessionTeardown exercises explicit program
// handle Stop overlapping with DisposeSession, verifying one terminal outcome
// per handle, no duplicate settlement, no stranded waiter, and sibling isolation.
func TestProgramExplicitStopOverlappingSessionTeardown(t *testing.T) {
	completed := make(chan bgprocess.Completion, 4)
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
	})
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
	supervisor := &ProgramSupervisor{Background: registry}

	ownedJobs := supervisor.Jobs("sess-overlap", "proj-1")
	siblingJobs := supervisor.Jobs("sess-sibling", "proj-1")
	admitted, err := ownedJobs.BeginProgram(t.Context(), ProgramJobOrigin{Tool: "command", ToolCallID: "call-ov", RootSessionID: "sess-overlap"})
	testutil.FailErr(t, "begin owned program", err)
	sibAdmitted, err := siblingJobs.BeginProgram(t.Context(), ProgramJobOrigin{Tool: "command", ToolCallID: "call-sib", RootSessionID: "sess-sibling"})
	testutil.FailErr(t, "begin sibling program", err)

	heldStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var releaseOnce sync.Once
	unlock := func() { releaseOnce.Do(func() { close(releaseCleanup) }) }
	t.Cleanup(unlock)

	root := toolcontract.OperationInvocation{SessionID: "sess-overlap", Identity: toolcontract.ExecutionIdentity{ProgramID: "prog-overlap", ExecutionID: "root"}}
	var jobHandle string
	fgDone := make(chan error, 1)
	go func() {
		_, runErr := supervisor.RunProgram(admitted, root, func(runCtx context.Context) (toolcontract.ProducerResult, error) {
			started, startErr := supervisor.StartJob(runCtx, root, "held-overlap", func(jobCtx context.Context, _ string, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
				noteJobNative(jobCtx, []string{"held-overlap"})
				close(heldStarted)
				<-jobCtx.Done()
				<-releaseCleanup
				return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted}, jobCtx.Err()
			})
			if startErr != nil {
				return toolcontract.ProducerResult{}, startErr
			}
			jobHandle = started.Handle
			<-runCtx.Done()
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted}, runCtx.Err()
		})
		fgDone <- runErr
	}()
	testutil.AwaitClosed(t, "held job started", heldStarted)

	// Start a sibling held job in another session to verify teardown isolation.
	sibStarted := make(chan struct{})
	sibRelease := make(chan struct{})
	t.Cleanup(func() { close(sibRelease) })
	sibRoot := toolcontract.OperationInvocation{SessionID: "sess-sibling", Identity: toolcontract.ExecutionIdentity{ProgramID: "prog-sib", ExecutionID: "root"}}
	_, err = supervisor.RunProgram(sibAdmitted, sibRoot, func(runCtx context.Context) (toolcontract.ProducerResult, error) {
		_, startErr := supervisor.StartJob(runCtx, sibRoot, "sib-job", func(jobCtx context.Context, _ string, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
			noteJobNative(jobCtx, []string{"sib-job"})
			close(sibStarted)
			select {
			case <-sibRelease:
				return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, nil
			case <-jobCtx.Done():
				return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted}, jobCtx.Err()
			}
		})
		return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, startErr
	})
	testutil.FailErr(t, "start sibling job", err)
	testutil.AwaitClosed(t, "sibling job started", sibStarted)

	var settleCalls atomic.Int32
	settled := make(chan bool, 2)
	supervisor.AfterJobs("prog-overlap", func(stopped bool) {
		settleCalls.Add(1)
		settled <- stopped
	})

	// Race explicit StopProgram and DisposeSession concurrently.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		supervisor.StopProgram("prog-overlap", bgprocess.TerminationStoppedByUser)
	}()
	disposeErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		disposeErr <- supervisor.DisposeSession(t.Context(), "sess-overlap")
	}()

	// Wait for foreground unwind, then release held job cleanup so both callers converge.
	<-fgDone
	unlock()
	wg.Wait()
	testutil.FailErr(t, "dispose session", <-disposeErr)

	if !testutil.Receive(t, "AfterJobs settled", settled) {
		t.Fatal("expected stopped=true on AfterJobs settlement")
	}
	if settleCalls.Load() != 1 {
		t.Fatalf("AfterJobs settled %d times, want 1", settleCalls.Load())
	}
	if registry.HasRunning("sess-overlap") {
		t.Fatalf("sess-overlap still has running jobs after stop+dispose (handle=%s)", jobHandle)
	}
	if !registry.HasRunning("sess-sibling") {
		t.Fatal("sibling session job was unexpectedly stopped by sess-overlap teardown")
	}
}
