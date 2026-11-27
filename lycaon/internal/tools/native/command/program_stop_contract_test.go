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
