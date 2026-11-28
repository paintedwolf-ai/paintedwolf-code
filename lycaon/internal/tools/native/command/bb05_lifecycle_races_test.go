//go:build unix

package command_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func bb05RootInvocation(sessionID, programID string) toolcontract.OperationInvocation {
	return toolcontract.OperationInvocation{
		SessionID: sessionID,
		Identity:  toolcontract.ExecutionIdentity{ProgramID: programID, ExecutionID: programID},
	}
}

// M07: Exercise both winners of child admission vs Stop(sessionID).
func TestBB05_M07_ChildAdmissionVsStopBothWinners(t *testing.T) {
	t.Run("admit_before_stop", func(t *testing.T) {
		sup := &command.ProgramSupervisor{}
		root := bb05RootInvocation("sess-child-1", "prog-child-1")
		admitted := make(chan struct{})
		stopped := make(chan struct{})
		var secondErr error

		done := make(chan error, 1)
		go func() {
			_, err := sup.RunProgram(t.Context(), root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
				if err := sup.AdmitChild(ctx); err != nil {
					return toolcontract.ProducerResult{}, fmt.Errorf("first admit failed: %w", err)
				}
				close(admitted)
				<-stopped
				secondErr = sup.AdmitChild(ctx)
				return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted}, nil
			})
			done <- err
		}()

		<-admitted
		ids := sup.Stop(root.SessionID)
		if len(ids) != 1 || ids[0] != root.Identity.ProgramID {
			t.Fatalf("Stop ids = %v, want [%s]", ids, root.Identity.ProgramID)
		}
		close(stopped)
		err := <-done
		if !errors.Is(err, command.ErrProgramStopped) {
			t.Fatalf("RunProgram err = %v, want ErrProgramStopped", err)
		}
		if secondErr == nil {
			t.Fatal("second AdmitChild after Stop succeeded")
		}
		var settled atomic.Int32
		var settledStopped atomic.Bool
		sup.AfterJobs(root.Identity.ProgramID, func(wasStopped bool) {
			settled.Add(1)
			settledStopped.Store(wasStopped)
		})
		if settled.Load() != 1 || !settledStopped.Load() {
			t.Fatalf("AfterJobs count=%d stopped=%v, want 1/true", settled.Load(), settledStopped.Load())
		}
	})

	t.Run("stop_before_admit", func(t *testing.T) {
		sup := &command.ProgramSupervisor{}
		root := bb05RootInvocation("sess-child-2", "prog-child-2")
		entered := make(chan struct{})
		stopDone := make(chan struct{})
		var admitErr error
		var childRan atomic.Bool

		done := make(chan error, 1)
		go func() {
			_, err := sup.RunProgram(t.Context(), root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
				close(entered)
				<-stopDone
				// Use WithoutCancel to isolate supervisor closed-admission check from ctx.Err().
				admitErr = sup.AdmitChild(context.WithoutCancel(ctx))
				if admitErr == nil {
					childRan.Store(true)
				}
				return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted}, nil
			})
			done <- err
		}()

		<-entered
		ids := sup.Stop(root.SessionID)
		if len(ids) != 1 || ids[0] != root.Identity.ProgramID {
			t.Fatalf("Stop ids = %v, want [%s]", ids, root.Identity.ProgramID)
		}
		close(stopDone)
		<-done
		reject := toolrejection.AsToolReject(admitErr)
		if reject == nil || reject.Code != toolrejection.ToolOwnerInterruptedCode {
			t.Fatalf("AdmitChild after Stop = %v, want %s", admitErr, toolrejection.ToolOwnerInterruptedCode)
		}
		if childRan.Load() {
			t.Fatal("child ran after Stop closed admission")
		}
	})
}

// M07: Exercise both winners of Hold admission vs Stop(sessionID).
func TestBB05_M07_HoldAdmissionVsStopBothWinners(t *testing.T) {
	t.Run("hold_before_stop", func(t *testing.T) {
		sup := &command.ProgramSupervisor{}
		root := bb05RootInvocation("sess-hold-1", "prog-hold-1")
		held := make(chan struct{})
		var jobCtx context.Context
		var release func()
		var stopCalls atomic.Int32

		_, err := sup.RunProgram(t.Context(), root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
			var setStop func(func(bgprocess.TerminationReason))
			var holdErr error
			jobCtx, setStop, release, holdErr = sup.Hold(ctx)
			if holdErr != nil {
				return toolcontract.ProducerResult{}, holdErr
			}
			setStop(func(bgprocess.TerminationReason) { stopCalls.Add(1) })
			close(held)
			return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted}, nil
		})
		testutil.FailErr(t, "run program with hold", err)
		<-held

		var settleCalls atomic.Int32
		var settleStopped atomic.Bool
		settledCh := make(chan struct{})
		sup.AfterJobs(root.Identity.ProgramID, func(wasStopped bool) {
			settleCalls.Add(1)
			settleStopped.Store(wasStopped)
			close(settledCh)
		})

		ids := sup.Stop(root.SessionID)
		if len(ids) != 1 || ids[0] != root.Identity.ProgramID {
			t.Fatalf("Stop ids = %v, want [%s]", ids, root.Identity.ProgramID)
		}
		if !errors.Is(context.Cause(jobCtx), command.ErrProgramStopped) {
			t.Fatalf("jobCtx cause = %v, want ErrProgramStopped", context.Cause(jobCtx))
		}
		if stopCalls.Load() != 1 {
			t.Fatalf("stop callback calls = %d, want 1", stopCalls.Load())
		}
		select {
		case <-settledCh:
			t.Fatal("AfterJobs fired before held job released")
		default:
		}
		release()
		release() // idempotent release
		<-settledCh
		if settleCalls.Load() != 1 || !settleStopped.Load() {
			t.Fatalf("AfterJobs calls=%d stopped=%v, want 1/true", settleCalls.Load(), settleStopped.Load())
		}
	})

	t.Run("stop_before_hold", func(t *testing.T) {
		sup := &command.ProgramSupervisor{}
		root := bb05RootInvocation("sess-hold-2", "prog-hold-2")
		entered := make(chan struct{})
		stopped := make(chan struct{})
		var holdErr error

		done := make(chan error, 1)
		go func() {
			_, err := sup.RunProgram(t.Context(), root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
				close(entered)
				<-stopped
				_, _, _, holdErr = sup.Hold(ctx)
				return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted}, nil
			})
			done <- err
		}()

		<-entered
		sup.Stop(root.SessionID)
		close(stopped)
		<-done
		reject := toolrejection.AsToolReject(holdErr)
		if reject == nil || reject.Code != toolrejection.ToolOwnerInterruptedCode {
			t.Fatalf("Hold after Stop = %v, want %s", holdErr, toolrejection.ToolOwnerInterruptedCode)
		}
		var settleCalls atomic.Int32
		sup.AfterJobs(root.Identity.ProgramID, func(bool) { settleCalls.Add(1) })
		if settleCalls.Load() != 1 {
			t.Fatalf("AfterJobs stranded after refused Hold: calls=%d", settleCalls.Load())
		}
	})
}

// M07 + ST02: Hold callback install before vs after Stop must deliver exactly
// one callback invocation across both handoff winners.
func TestBB05_M07_HoldCallbackInstallBeforeAndAfterStop(t *testing.T) {
	t.Run("callback_before_stop", func(t *testing.T) {
		sup := &command.ProgramSupervisor{}
		root := bb05RootInvocation("sess-cb-1", "prog-cb-1")
		var jobCtx context.Context
		var release func()
		var cbCount atomic.Int32

		_, err := sup.RunProgram(t.Context(), root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
			var setStop func(func(bgprocess.TerminationReason))
			var holdErr error
			jobCtx, setStop, release, holdErr = sup.Hold(ctx)
			if holdErr != nil {
				return toolcontract.ProducerResult{}, holdErr
			}
			setStop(func(bgprocess.TerminationReason) { cbCount.Add(1) })
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, nil
		})
		testutil.FailErr(t, "run program", err)

		sup.Stop(root.SessionID)
		if cbCount.Load() != 1 {
			t.Fatalf("callback before Stop invoked %d times, want 1", cbCount.Load())
		}
		if !errors.Is(context.Cause(jobCtx), command.ErrProgramStopped) {
			t.Fatalf("jobCtx cause = %v, want ErrProgramStopped", context.Cause(jobCtx))
		}
		var settled atomic.Int32
		sup.AfterJobs(root.Identity.ProgramID, func(bool) { settled.Add(1) })
		release()
		if settled.Load() != 1 {
			t.Fatalf("settled count = %d, want 1", settled.Load())
		}
	})

	t.Run("callback_after_stop", func(t *testing.T) {
		sup := &command.ProgramSupervisor{}
		root := bb05RootInvocation("sess-cb-2", "prog-cb-2")
		var jobCtx context.Context
		var setStop func(func(bgprocess.TerminationReason))
		var release func()
		var cbCount atomic.Int32

		_, err := sup.RunProgram(t.Context(), root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
			var holdErr error
			jobCtx, setStop, release, holdErr = sup.Hold(ctx)
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, holdErr
		})
		testutil.FailErr(t, "run program", err)

		// Stop runs between Hold registration and setStop installation.
		sup.Stop(root.SessionID)
		setStop(func(bgprocess.TerminationReason) { cbCount.Add(1) })

		if !errors.Is(context.Cause(jobCtx), command.ErrProgramStopped) {
			t.Fatalf("jobCtx cause = %v, want ErrProgramStopped", context.Cause(jobCtx))
		}
		if cbCount.Load() != 1 {
			t.Fatalf("callback after Stop invoked %d times, want 1 (ST02 latch)", cbCount.Load())
		}

		var settled atomic.Int32
		var wasStopped atomic.Bool
		sup.AfterJobs(root.Identity.ProgramID, func(stopped bool) {
			settled.Add(1)
			wasStopped.Store(stopped)
		})
		release()
		if settled.Load() != 1 || !wasStopped.Load() {
			t.Fatalf("settled=%d stopped=%v, want 1/true", settled.Load(), wasStopped.Load())
		}
	})
}
