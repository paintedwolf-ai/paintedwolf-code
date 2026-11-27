//go:build unix

package command_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

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

	// Interrupt only prog-a1's foreground; its held job, sibling prog-a2, and sess-b survive.
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

	// Stop("sess-a") ends prog-a1's held job and prog-a2, while sess-b remains unaffected.
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

	// Sibling session B finishes cleanly and settles with stopped == false.
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
