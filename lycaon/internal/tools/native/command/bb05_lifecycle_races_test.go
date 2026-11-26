//go:build unix

package command_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// TestBB05HelperProcess is the fixture-owned OS process used by M08 tests.
// In "parent" mode it spawns a "descendant" copy of itself that holds an flock
// so descendant settlement is directly observable without relying on PID reuse.
func TestBB05HelperProcess(t *testing.T) {
	mode := os.Getenv("PW_BB05_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "descendant":
		lockPath := os.Getenv("PW_BB05_LOCK")
		pidPath := os.Getenv("PW_BB05_DESC_PID")
		lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil || syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX) != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(3)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "parent":
		exe, err := os.Executable()
		if err != nil {
			os.Exit(4)
		}
		desc := exec.Command(exe, "-test.run=^TestBB05HelperProcess$")
		desc.Env = append(os.Environ(), "PW_BB05_HELPER=descendant")
		if err := desc.Start(); err != nil {
			os.Exit(5)
		}
		descPIDPath := os.Getenv("PW_BB05_DESC_PID")
		for range 500 {
			if _, err := os.Stat(descPIDPath); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := os.WriteFile(os.Getenv("PW_BB05_PARENT_PID"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(6)
		}
		fmt.Println("parent-ready")
		releasePath := os.Getenv("PW_BB05_RELEASE")
		for i := range 2000 {
			if releasePath != "" {
				if _, err := os.Stat(releasePath); err == nil {
					for j := range 16 {
						fmt.Printf("burst-%d\n", j)
					}
					_ = desc.Process.Kill()
					_, _ = desc.Process.Wait()
					os.Exit(0)
				}
			}
			if i%50 == 0 {
				fmt.Printf("tick-%d\n", i/50)
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = desc.Process.Kill()
		_, _ = desc.Process.Wait()
		os.Exit(0)
	}
}

func bb05LockFree(t *testing.T, lockPath string) bool {
	t.Helper()
	f, err := os.OpenFile(lockPath, os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return false
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true
}

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
			var setStop func(func())
			var holdErr error
			jobCtx, setStop, release, holdErr = sup.Hold(ctx)
			if holdErr != nil {
				return toolcontract.ProducerResult{}, holdErr
			}
			setStop(func() { stopCalls.Add(1) })
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

// M07 + Static Audit Finding 1: Hold callback install before vs after Stop.
func TestBB05_M07_HoldCallbackInstallBeforeAndAfterStop(t *testing.T) {
	t.Run("callback_before_stop", func(t *testing.T) {
		sup := &command.ProgramSupervisor{}
		root := bb05RootInvocation("sess-cb-1", "prog-cb-1")
		var jobCtx context.Context
		var release func()
		var cbCount atomic.Int32

		_, err := sup.RunProgram(t.Context(), root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
			var setStop func(func())
			var holdErr error
			jobCtx, setStop, release, holdErr = sup.Hold(ctx)
			if holdErr != nil {
				return toolcontract.ProducerResult{}, holdErr
			}
			setStop(func() { cbCount.Add(1) })
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
		var setStop func(func())
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
		setStop(func() { cbCount.Add(1) })

		// Baseline guarantees jobCtx cancellation and single terminal settlement.
		// Audit finding: supervisedJob lacks an ended latch, so cbCount stays 0
		// when setStop runs after end(); ST01-ST03 / fix should latch ended state.
		if !errors.Is(context.Cause(jobCtx), command.ErrProgramStopped) {
			t.Fatalf("jobCtx cause = %v, want ErrProgramStopped", context.Cause(jobCtx))
		}
		t.Logf("baseline observation (audit finding 1): setStop installed after Stop -> cbCount=%d", cbCount.Load())

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

// M07: Duplicate Stop and foreground already done with held work.
func TestBB05_M07_DuplicateStopAndForegroundDoneWithHeldWork(t *testing.T) {
	sup := &command.ProgramSupervisor{}
	root := bb05RootInvocation("sess-dup", "prog-dup")
	var jobCtx context.Context
	var release func()
	var stopCount atomic.Int32

	_, err := sup.RunProgram(t.Context(), root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
		var setStop func(func())
		var holdErr error
		jobCtx, setStop, release, holdErr = sup.Hold(ctx)
		if holdErr == nil {
			setStop(func() { stopCount.Add(1) })
		}
		return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted}, holdErr
	})
	testutil.FailErr(t, "foreground done with held work", err)

	// Foreground is already done; Interrupt must report false.
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
			var setStop func(func())
			var holdErr error
			a1JobCtx, setStop, a1JobRelease, holdErr = sup.Hold(ctx)
			if holdErr != nil {
				return toolcontract.ProducerResult{}, holdErr
			}
			setStop(func() {})
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

// M07: Continued-program Stop on current baseline with explicit ST01-ST03 pin.
// Prerequisite pin (ST01-ST03): ProgramSupervisor.StopProgram currently calls
// Interrupt + snapshotted job.end without setting program.closed = true.
// Once ST01-ST03 lands the continued-program Stop fence, StopProgram must also
// close admission for concurrent AdmitChild and Hold calls.
func TestBB05_M07_ContinuedProgramStopPrerequisiteST01_ST03(t *testing.T) {
	f := programtest.New(t, programtest.Options{})
	clock := programtest.NewManualClock()
	f.Runtime.Clock = clock

	entered := make(chan struct{})
	var laterMarker atomic.Bool
	f.Runtime.Evaluate = func(ctx context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		_, _ = fmt.Fprint(ports.Streams.Output, "before-stop\n")
		close(entered)
		<-ctx.Done()
		if ctx.Err() == nil {
			laterMarker.Store(true)
			_, _ = fmt.Fprint(ports.Streams.Output, "after-stop\n")
		}
		return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
	}

	done := make(chan callResult, 1)
	go func() {
		out, err := f.Call(t, t.Context(), "continued-work; later-marker")
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
	if !strings.Contains(stdout, "before-stop\n") || strings.Contains(stdout, "after-stop") || laterMarker.Load() {
		t.Fatalf("continued program output mismatch: stdout=%q laterMarker=%v", stdout, laterMarker.Load())
	}
	snap, err := f.Background.Output.ReadCommandOutput(t.Context(), f.Session, running.Handle, 0)
	testutil.FailErr(t, "read continued program output", err)
	if snap.ProgramStopCode != toolrejection.ToolOwnerInterruptedCode || snap.TerminationReason != bgprocess.TerminationStoppedByAgent {
		t.Fatalf("continued program terminal state = %+v", snap)
	}
}

// M08: Native job + fixture-owned helper OS process with observable descendant.
// Exercises output read vs exit race, parked wait interrupt, single-job stop,
// stable identity distinction (shell job token vs OS PID vs registry handle),
// descendant settlement, baseline cleanup, and session reuse under -race.
func TestBB05_M08_ProcessAndWaitReadRaces(t *testing.T) {
	exe, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)

	f := newProgramFixture(t)
	clock := programtest.NewManualClock()
	f.Runtime.Clock = clock
	parking := newRecordingParking()
	f.Runtime.Parking = parking

	lockPath := filepath.Join(f.Dir, "descendant.lock")
	parentPIDPath := filepath.Join(f.Dir, "parent.pid")
	descPIDPath := filepath.Join(f.Dir, "descendant.pid")

	// 1. Start a native background job (%1) and a fixture-owned external helper OS process (%2) with a descendant.
	outNative, err := f.call(t, t.Context(), "sleep 60 &")
	testutil.FailErr(t, "start native job", err)
	_ = decodeOutcome(t, outNative)

	helperCmd := fmt.Sprintf(
		"PW_BB05_HELPER=parent PW_BB05_LOCK=%s PW_BB05_PARENT_PID=%s PW_BB05_DESC_PID=%s external -- %s '-test.run=^TestBB05HelperProcess$' &",
		strconv.Quote(lockPath), strconv.Quote(parentPIDPath), strconv.Quote(descPIDPath), strconv.Quote(exe),
	)
	outExt, err := f.call(t, t.Context(), helperCmd)
	testutil.FailErr(t, "start external helper job", err)
	_ = decodeOutcome(t, outExt)

	testutil.WaitFor(t, "helper parent and descendant ready", func() bool {
		_, errP := os.Stat(parentPIDPath)
		_, errD := os.Stat(descPIDPath)
		return errP == nil && errD == nil
	})
	if bb05LockFree(t, lockPath) {
		t.Fatal("descendant did not hold its observable lock while running")
	}

	parentPIDBytes, err := os.ReadFile(parentPIDPath)
	testutil.FailErr(t, "read parent pid", err)
	descPIDBytes, err := os.ReadFile(descPIDPath)
	testutil.FailErr(t, "read descendant pid", err)
	parentPID, err := strconv.Atoi(strings.TrimSpace(string(parentPIDBytes)))
	testutil.FailErr(t, "parse parent pid", err)
	descPID, err := strconv.Atoi(strings.TrimSpace(string(descPIDBytes)))
	testutil.FailErr(t, "parse descendant pid", err)
	if parentPID <= 0 || descPID <= 0 || parentPID == descPID {
		t.Fatalf("invalid OS PIDs: parent=%d descendant=%d", parentPID, descPID)
	}

	// Verify stable distinction among shell job token (%1/%2), OS PID, and registry handle.
	active := f.Background.ActiveJobs(f.Session)
	if len(active) != 2 {
		t.Fatalf("active jobs = %d, want 2", len(active))
	}
	jobs := f.Runtime.Supervisor.Jobs(f.Session, "default")
	h1, err := jobs.Resolve("%1")
	testutil.FailErr(t, "resolve %1", err)
	h2, err := jobs.Resolve("%2")
	testutil.FailErr(t, "resolve %2", err)
	hByPID, err := jobs.Resolve(strconv.Itoa(parentPID))
	testutil.FailErr(t, "resolve external helper by OS PID", err)
	if h1 == h2 || h2 != hByPID {
		t.Fatalf("job token / handle / OS PID mismatch: %%1=%q %%2=%q pid(%d)=%q", h1, h2, parentPID, hByPID)
	}
	if h1 == "%1" || h2 == "%2" || h2 == strconv.Itoa(parentPID) {
		t.Fatalf("registry handles collapsed into shell tokens or OS PIDs: h1=%q h2=%q", h1, h2)
	}

	// 2. Interrupt a parked wait on %1; verify wait settles interrupted while %1 and %2 keep running.
	waitDone := make(chan callResult, 1)
	go func() {
		content, callErr := f.call(t, t.Context(), "wait %1")
		waitDone <- callResult{content, callErr}
	}()
	testutil.WaitFor(t, "parked wait timer scheduled", func() bool {
		return clock.Pending() >= 2
	})
	clock.Advance(toolcontract.ProgramParkThreshold)
	parkedRes := <-waitDone
	testutil.FailErr(t, "park wait %1", parkedRes.err)
	var parked command.ParkedProgramResult
	testutil.FailErr(t, "decode parked wait", json.Unmarshal([]byte(parkedRes.content), &parked))
	if parked.State != "parked" {
		t.Fatalf("expected parked wait, got %+v", parked)
	}
	recPark := <-parking.parked
	interruptedRaw := recPark.park.Interrupt(t.Context())
	interrupted := decodeOutcome(t, interruptedRaw)
	if interrupted.State != "interrupted" || interrupted.Status != 130 || interrupted.StopCode != toolrejection.ToolOwnerInterruptedCode {
		t.Fatalf("interrupted parked wait = %+v", interrupted)
	}
	<-recPark.settled
	if len(f.Background.ActiveJobs(f.Session)) != 2 {
		t.Fatalf("interrupting parked wait killed background jobs: active=%d", len(f.Background.ActiveJobs(f.Session)))
	}

	// 3. Stop only the external helper job (%2) while concurrently racing output reads under -race.
	var readerWG sync.WaitGroup
	var readErrs atomic.Int32
	for range 4 {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			var cursor int64
			for range 64 {
				snap, rErr := f.Background.Output.ReadCommandOutput(t.Context(), f.Session, h2, cursor)
				if rErr != nil {
					readErrs.Add(1)
					return
				}
				if snap.Output.Next < cursor {
					readErrs.Add(1)
					return
				}
				cursor = snap.Output.Next
				if !snap.Output.Running && snap.Output.ExitCode != nil {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}

	_, err = f.Background.Lifecycle.Stop(f.Session, h2, bgprocess.TerminationStoppedByAgent)
	testutil.FailErr(t, "stop external helper job", err)
	_, _, err = f.Background.Lifecycle.Wait(t.Context(), f.Session, h2)
	testutil.FailErr(t, "wait stopped external helper job", err)
	readerWG.Wait()
	if readErrs.Load() != 0 {
		t.Fatalf("concurrent output read/exit race failed with %d errors", readErrs.Load())
	}

	// Observed descendant must settle and release its lock; sibling native job (%1) must remain active.
	testutil.WaitFor(t, "descendant process settled", func() bool {
		return bb05LockFree(t, lockPath)
	})
	remaining := f.Background.ActiveJobs(f.Session)
	if len(remaining) != 1 || remaining[0].Handle != h1 {
		t.Fatalf("stopping %%2 affected sibling %%1: remaining=%+v", remaining)
	}

	// 4. Stop the remaining native job and verify capacity/waiters return to baseline.
	_, err = f.Background.Lifecycle.Stop(f.Session, h1, bgprocess.TerminationStoppedByAgent)
	testutil.FailErr(t, "stop native job", err)
	_, _, err = f.Background.Lifecycle.Wait(t.Context(), f.Session, h1)
	testutil.FailErr(t, "wait stopped native job", err)
	if activeAfter := f.Background.ActiveJobs(f.Session); len(activeAfter) != 0 {
		t.Fatalf("capacity did not return to baseline: active=%+v", activeAfter)
	}

	// 5. Also race output read with a clean exit of a short-lived helper OS process.
	releaseFile := filepath.Join(f.Dir, "release-clean")
	cleanLock := filepath.Join(f.Dir, "clean-desc.lock")
	cleanParentPID := filepath.Join(f.Dir, "clean-parent.pid")
	cleanDescPID := filepath.Join(f.Dir, "clean-desc.pid")
	cleanCmd := fmt.Sprintf(
		"PW_BB05_HELPER=parent PW_BB05_LOCK=%s PW_BB05_PARENT_PID=%s PW_BB05_DESC_PID=%s PW_BB05_RELEASE=%s external -- %s '-test.run=^TestBB05HelperProcess$' &",
		strconv.Quote(cleanLock), strconv.Quote(cleanParentPID), strconv.Quote(cleanDescPID), strconv.Quote(releaseFile), strconv.Quote(exe),
	)
	_, err = f.call(t, t.Context(), cleanCmd)
	testutil.FailErr(t, "start clean-exit helper", err)
	testutil.WaitFor(t, "clean-exit helper ready", func() bool {
		_, errP := os.Stat(cleanParentPID)
		return errP == nil
	})
	cleanJobs := f.Background.ActiveJobs(f.Session)
	if len(cleanJobs) != 1 {
		t.Fatalf("expected 1 clean-exit helper job, got %d", len(cleanJobs))
	}
	cleanHandle := cleanJobs[0].Handle
	var cleanWG sync.WaitGroup
	var cleanReadFailed atomic.Bool
	for range 4 {
		cleanWG.Add(1)
		go func() {
			defer cleanWG.Done()
			var cur int64
			for range 128 {
				snap, rErr := f.Background.Output.ReadCommandOutput(t.Context(), f.Session, cleanHandle, cur)
				if rErr != nil || snap.Output.Next < cur {
					cleanReadFailed.Store(true)
					return
				}
				cur = snap.Output.Next
				if !snap.Output.Running && snap.Output.ExitCode != nil {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	testutil.FailErr(t, "release clean-exit helper", os.WriteFile(releaseFile, nil, 0o600))
	exitCode, _, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, cleanHandle)
	testutil.FailErr(t, "wait clean-exit helper", err)
	cleanWG.Wait()
	if exitCode != 0 || cleanReadFailed.Load() {
		t.Fatalf("clean-exit output/exit race: exit=%d readFailed=%v", exitCode, cleanReadFailed.Load())
	}
	testutil.WaitFor(t, "clean-exit descendant settled", func() bool {
		return bb05LockFree(t, cleanLock)
	})

	// 6. Verify subsequent session reuse succeeds cleanly.
	reusedRaw, err := f.call(t, t.Context(), "echo session-reused-ok")
	testutil.FailErr(t, "reuse session after job stops", err)
	reused := decodeOutcome(t, reusedRaw)
	if reused.State != "completed" || reused.Status != 0 || reused.Stdout != "session-reused-ok\n" {
		t.Fatalf("session reuse outcome = %+v", reused)
	}
}
