//go:build unix

package command_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// M07 (scoped cancellation witness): Demonstrates continued-program cancellation
// and terminal delivery after Lifecycle.Stop on a substitute evaluator.
// This witness does not establish the later-statement or admission fence.
func TestBB05_M07_ContinuedProgramStopCancellationWitness(t *testing.T) {
	f := programtest.New(t, programtest.Options{})
	clock := programtest.NewManualClock()
	f.Runtime.Clock = clock

	entered := make(chan struct{})
	f.Runtime.Evaluate = func(ctx context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		_, _ = fmt.Fprint(ports.Streams.Output, "before-stop\n")
		close(entered)
		<-ctx.Done()
		return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
	}

	done := make(chan callResult, 1)
	go func() {
		out, err := f.Call(t, t.Context(), "continued-work")
		done <- callResult{out, err}
	}()

	<-entered
	clock.Advance(toolcontract.ProgramInlineWait)
	got := <-done
	testutil.FailErr(t, "promote continued program", got.err)

	var running command.RunningProgramResult
	testutil.FailErr(t, "decode running result", json.Unmarshal([]byte(got.content), &running))
	if running.State != "running" || running.Handle == "" {
		t.Fatalf("expected continued running handle, got %+v", running)
	}

	_, err := f.Background.Lifecycle.Stop(f.Session, running.Handle, bgprocess.TerminationStoppedByAgent)
	testutil.FailErr(t, "stop continued program", err)

	_, stopped, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
	testutil.FailErr(t, "wait stopped continued program", err)
	if !stopped {
		t.Fatal("continued program Wait reported stopped=false")
	}

	stdout, _ := continuationStreams(t, f, running.Handle)
	if !strings.Contains(stdout, "before-stop\n") {
		t.Fatalf("continued program lost earlier output: stdout=%q", stdout)
	}
	snap, err := f.Background.Output.ReadCommandOutput(t.Context(), f.Session, running.Handle, 0)
	testutil.FailErr(t, "read continued program output", err)
	if snap.ProgramStopCode != toolrejection.ToolOwnerInterruptedCode || snap.TerminationReason != bgprocess.TerminationStoppedByAgent {
		t.Fatalf("continued program terminal state = %+v", snap)
	}
}

// M07 (real dispatch + positive counterpart + ST01-ST03 admission fence pin):
// Drives real commandprogram.Evaluate over a gated native stage (`head; echo later-marker`).
// The positive subtest proves `echo later-marker` executes when admitted after continuation.
// The stopped subtest exercises Lifecycle.Stop after public continuation acknowledgment and
// pins the ST01-ST03 prerequisite for closing program admission in StopProgram.
func TestBB05_M07_ContinuedProgramStopDispatchAndPositiveCounterpart(t *testing.T) {
	for _, stop := range []bool{false, true} {
		name := "positive_admitted_runs_later_marker"
		if stop {
			name = "stopped_after_continuation_acknowledgment"
		}
		t.Run(name, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})

			f := programtest.New(t, programtest.Options{Natives: func(string) map[string]command.NativeOperation {
				op := firstLineOperation()
				prepare := op.Prepare
				op.Prepare = func(ctx context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
					prepared, err := prepare(ctx, paths, dispatch, request)
					if err != nil {
						return command.NativePrepared{}, err
					}
					prepared.Invoke = func(ctx context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
						_, _ = fmt.Fprint(streams.Output, "before-gate\n")
						close(entered)
						select {
						case <-release:
							return toolcontract.ProducerResult{
								Status:              0,
								Terminal:            toolcontract.ProducerCompleted,
								Complete:            true,
								PublicationEligible: true,
								EffectCertainty:     toolcontract.NoEffect,
							}, nil
						case <-ctx.Done():
							return toolcontract.ProducerResult{
								Status:          130,
								Terminal:        toolcontract.ProducerInterrupted,
								StopCode:        toolrejection.ToolOwnerInterruptedCode,
								EffectCertainty: toolcontract.NoEffect,
							}, ctx.Err()
						}
					}
					return prepared, nil
				}
				return map[string]command.NativeOperation{"head": op}
			}})

			clock := programtest.NewManualClock()
			f.Runtime.Clock = clock

			done := make(chan callResult, 1)
			go func() {
				out, err := f.Call(t, t.Context(), "head; echo later-marker")
				done <- callResult{out, err}
			}()

			<-entered
			clock.Advance(toolcontract.ProgramInlineWait)
			got := <-done
			testutil.FailErr(t, "promote continued program", got.err)

			var running command.RunningProgramResult
			testutil.FailErr(t, "decode running result", json.Unmarshal([]byte(got.content), &running))
			if running.State != "running" || running.Handle == "" {
				t.Fatalf("expected continued running handle, got %+v", running)
			}

			if stop {
				_, err := f.Background.Lifecycle.Stop(f.Session, running.Handle, bgprocess.TerminationStoppedByAgent)
				testutil.FailErr(t, "stop continued program", err)
			} else {
				close(release)
			}

			status, wasStopped, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
			testutil.FailErr(t, "join continued program", err)
			stdout, _ := continuationStreams(t, f, running.Handle)

			if !stop {
				if status != 0 || wasStopped || stdout != "before-gate\nlater-marker\n" {
					t.Fatalf("positive continuation: status=%d stopped=%v stdout=%q", status, wasStopped, stdout)
				}
				return
			}

			if !wasStopped {
				t.Fatal("stopped continuation Wait reported stopped=false")
			}
			if !strings.Contains(stdout, "before-gate\n") || strings.Contains(stdout, "later-marker") {
				t.Fatalf("stopped continuation output mismatch: stdout=%q", stdout)
			}
			snap, err := f.Background.Output.ReadCommandOutput(t.Context(), f.Session, running.Handle, 0)
			testutil.FailErr(t, "read stopped continuation output", err)
			if snap.ProgramStopCode != toolrejection.ToolOwnerInterruptedCode || snap.TerminationReason != bgprocess.TerminationStoppedByAgent {
				t.Fatalf("stopped continuation terminal state = %+v", snap)
			}
		})
	}
}

// M07: Duplicate Stop and foreground already done with held work.
func TestBB05_M07_DuplicateStopAndForegroundDoneWithHeldWork(t *testing.T) {
	sup := &command.ProgramSupervisor{}
	root := bb05RootInvocation("sess-dup", "prog-dup")
	var jobCtx context.Context
	var release func()
	var stopCount atomic.Int32

	_, err := sup.RunProgram(t.Context(), root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
		var setStop func(func(bgprocess.TerminationReason))
		var holdErr error
		jobCtx, setStop, release, holdErr = sup.Hold(ctx)
		if holdErr == nil {
			setStop(func(bgprocess.TerminationReason) { stopCount.Add(1) })
		}
		return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, holdErr
	})
	testutil.FailErr(t, "foreground done with held work", err)

	if sup.Interrupt(root.Identity.ProgramID) {
		t.Fatal("Interrupt reported true after foreground completed")
	}

	var settleCount atomic.Int32
	var settleStopped atomic.Bool
	sup.AfterJobs(root.Identity.ProgramID, func(stopped bool) {
		settleCount.Add(1)
		settleStopped.Store(stopped)
	})
	if settleCount.Load() != 0 {
		t.Fatal("AfterJobs fired while held work is still active")
	}

	first := sup.Stop(root.SessionID)
	second := sup.Stop(root.SessionID)
	if len(first) != 1 || first[0] != root.Identity.ProgramID {
		t.Fatalf("first Stop = %v, want [%s]", first, root.Identity.ProgramID)
	}
	if len(second) != 0 {
		t.Fatalf("duplicate Stop = %v, want empty", second)
	}
	if stopCount.Load() != 1 || !errors.Is(context.Cause(jobCtx), command.ErrProgramStopped) {
		t.Fatalf("stopCount=%d cause=%v", stopCount.Load(), context.Cause(jobCtx))
	}

	release()
	if settleCount.Load() != 1 || !settleStopped.Load() {
		t.Fatalf("single terminal settlement failed: count=%d stopped=%v", settleCount.Load(), settleStopped.Load())
	}
}

// M07: Sibling program/job/session isolation and foreground interrupt as separate controls.
func TestBB05_M07_SiblingIsolationAndForegroundInterrupt(t *testing.T) {
	sup := &command.ProgramSupervisor{}
	rootA1 := bb05RootInvocation("sess-a", "prog-a1")
	rootA2 := bb05RootInvocation("sess-a", "prog-a2")
	rootB1 := bb05RootInvocation("sess-b", "prog-b1")

	a1Ready, a2Ready, b1Ready := make(chan struct{}), make(chan struct{}), make(chan struct{})
	b1Release := make(chan struct{})
	var a1JobCtx context.Context
	var a1JobRelease func()

	a1Done := make(chan error, 1)
	go func() {
		_, err := sup.RunProgram(t.Context(), rootA1, func(ctx context.Context) (toolcontract.ProducerResult, error) {
			var setStop func(func(bgprocess.TerminationReason))
			var holdErr error
			a1JobCtx, setStop, a1JobRelease, holdErr = sup.Hold(ctx)
			if holdErr != nil {
				return toolcontract.ProducerResult{}, holdErr
			}
			setStop(func(bgprocess.TerminationReason) {})
			close(a1Ready)
			<-ctx.Done()
			return toolcontract.ProducerResult{Status: 130}, ctx.Err()
		})
		a1Done <- err
	}()

	a2Done := make(chan error, 1)
	go func() {
		_, err := sup.RunProgram(t.Context(), rootA2, func(ctx context.Context) (toolcontract.ProducerResult, error) {
			close(a2Ready)
			<-ctx.Done()
			return toolcontract.ProducerResult{Status: 130}, ctx.Err()
		})
		a2Done <- err
	}()

	b1Done := make(chan error, 1)
	go func() {
		_, err := sup.RunProgram(t.Context(), rootB1, func(ctx context.Context) (toolcontract.ProducerResult, error) {
			close(b1Ready)
			<-b1Release
			if err := sup.AdmitChild(ctx); err != nil {
				return toolcontract.ProducerResult{}, err
			}
			return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted}, nil
		})
		b1Done <- err
	}()

	<-a1Ready
	<-a2Ready
	<-b1Ready

	if !sup.Interrupt(rootA1.Identity.ProgramID) {
		t.Fatal("Interrupt(prog-a1) returned false")
	}
	if err := <-a1Done; !errors.Is(err, command.ErrProgramInterrupted) {
		t.Fatalf("prog-a1 err = %v, want ErrProgramInterrupted", err)
	}
	if a1JobCtx.Err() != nil {
		t.Fatalf("foreground interrupt canceled held & job: %v", a1JobCtx.Err())
	}
	select {
	case err := <-a2Done:
		t.Fatalf("sibling prog-a2 ended on prog-a1 interrupt: %v", err)
	case err := <-b1Done:
		t.Fatalf("sibling session prog-b1 ended on prog-a1 interrupt: %v", err)
	default:
	}

	stoppedA := sup.Stop("sess-a")
	if len(stoppedA) != 2 {
		t.Fatalf("Stop(sess-a) = %v, want 2 programs", stoppedA)
	}
	if !errors.Is(context.Cause(a1JobCtx), command.ErrProgramStopped) {
		t.Fatalf("held job cause = %v, want ErrProgramStopped", context.Cause(a1JobCtx))
	}
	a1JobRelease()
	if err := <-a2Done; !errors.Is(err, command.ErrProgramStopped) {
		t.Fatalf("prog-a2 err = %v, want ErrProgramStopped", err)
	}

	close(b1Release)
	testutil.FailErr(t, "sibling session B run", <-b1Done)
	var b1Settled atomic.Int32
	var b1Stopped atomic.Bool
	sup.AfterJobs(rootB1.Identity.ProgramID, func(stopped bool) {
		b1Settled.Add(1)
		b1Stopped.Store(stopped)
	})
	if b1Settled.Load() != 1 || b1Stopped.Load() {
		t.Fatalf("sibling session B settlement count=%d stopped=%v, want 1/false", b1Settled.Load(), b1Stopped.Load())
	}
}
