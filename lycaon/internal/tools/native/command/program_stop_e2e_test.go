package command_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	"github.com/lycaon/lycaon/internal/session/projectcontrol"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubProjects struct {
	byID map[string]*project.Project
}

func (s stubProjects) Get(_ context.Context, id string) (*project.Project, error) {
	p, ok := s.byID[id]
	if !ok {
		return nil, project.ErrNotFound
	}
	return p, nil
}

func runCommandStopTool(t *testing.T, reg *bgprocess.Registry, sessionID, handle string) (toolkit.ProcessStopResult, error) {
	t.Helper()
	tool := &command.CommandStopTool{Registry: reg}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sessionID}}
	raw, err := tool.Run(t.Context(), map[string]any{"handle": handle}, tc)
	if err != nil {
		return toolkit.ProcessStopResult{}, err
	}
	var out toolkit.ProcessStopResult
	testutil.FailErr(t, "decode command_stop result", json.Unmarshal([]byte(raw), &out))
	return out, nil
}

func runCommandOutputTool(t *testing.T, reg *bgprocess.Registry, sessionID, handle string) map[string]any {
	t.Helper()
	tool := &command.CommandOutputTool{Registry: reg}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sessionID}}
	raw, err := tool.Run(t.Context(), map[string]any{"handle": handle}, tc)
	testutil.FailErr(t, "run command_output", err)
	var out map[string]any
	testutil.FailErr(t, "decode command_output", json.Unmarshal([]byte(raw), &out))
	return out
}

// TestProgramStopWinsAdmissionAndAllowsSubsequentCommand verifies that at the
// instant Stop acknowledges (both via command_stop and processcontrol),
// AdmitChild and Hold are refused, no subsequent statement runs, and another
// command in the same session completes normally.
func TestProgramStopWinsAdmissionAndAllowsSubsequentCommand(t *testing.T) {
	for _, viaUser := range []bool{false, true} {
		name := "agent_command_stop"
		wantReason := bgprocess.TerminationStoppedByAgent
		if viaUser {
			name = "user_processcontrol"
			wantReason = bgprocess.TerminationStoppedByUser
		}
		t.Run(name, func(t *testing.T) {
			completed := make(chan bgprocess.Completion, 2)
			registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
				Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
			})
			registry.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
			t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })

			f := programtest.New(t, programtest.Options{Background: registry})
			clock := programtest.NewManualClock()
			f.Runtime.Clock = clock

			entered := make(chan context.Context, 1)
			releaseCleanup := make(chan struct{})
			var releaseOnce sync.Once
			unlockCleanup := func() { releaseOnce.Do(func() { close(releaseCleanup) }) }
			t.Cleanup(unlockCleanup)

			laterMarker := filepath.Join(f.Dir, "later-statement.txt")
			f.Runtime.Evaluate = func(ctx context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
				_, _ = fmt.Fprint(ports.Streams.Output, "started\n")
				entered <- ctx
				<-ctx.Done()
				<-releaseCleanup
				// Attempt late admission after stop; must be refused.
				if err := f.Runtime.Supervisor.AdmitChild(ctx); err == nil {
					_ = os.WriteFile(laterMarker, []byte("late"), 0o644)
				}
				return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
			}

			done := make(chan callResult, 1)
			go func() {
				s, e := f.Call(t, t.Context(), "server; echo later")
				done <- callResult{s, e}
			}()
			progCtx := testutil.Receive(t, "program entered", entered)
			clock.Advance(toolcontract.ProgramInlineWait)
			got := testutil.Receive(t, "continue result", done)
			testutil.FailErr(t, "continue program", got.err)
			var running command.RunningProgramResult
			testutil.FailErr(t, "decode running result", json.Unmarshal([]byte(got.content), &running))

			if viaUser {
				pc := &processcontrol.Service{Background: registry}
				res, err := pc.StopBackgroundProcess(f.Session, running.Handle)
				testutil.FailErr(t, "user stop", err)
				if !res.StopRequested || !res.Running || res.TerminationReason != api.BackgroundProcessTerminationReason(wantReason) {
					t.Fatalf("user stop ack = %+v", res)
				}
			} else {
				res, err := runCommandStopTool(t, registry, f.Session, running.Handle)
				testutil.FailErr(t, "agent command_stop", err)
				if !res.StopRequested || !res.Running || res.TerminationReason != string(wantReason) {
					t.Fatalf("agent stop ack = %+v", res)
				}
			}

			// Immediately at acknowledgment (while cleanup is still held), AdmitChild and Hold must be refused.
			if err := f.Runtime.Supervisor.AdmitChild(progCtx); err == nil {
				t.Fatal("AdmitChild succeeded after Stop acknowledgment")
			}
			if _, _, _, err := f.Runtime.Supervisor.Hold(progCtx); err == nil {
				t.Fatal("Hold succeeded after Stop acknowledgment")
			}

			unlockCleanup()
			_, stopped, err := registry.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
			testutil.FailErr(t, "wait stopped program", err)
			if !stopped {
				t.Fatal("expected stopped=true on Wait")
			}
			c := testutil.Receive(t, "completion", completed)
			if c.TerminationReason != wantReason {
				t.Fatalf("completion reason = %q, want %q", c.TerminationReason, wantReason)
			}
			if _, statErr := os.Stat(laterMarker); !os.IsNotExist(statErr) {
				t.Fatalf("later statement marker existed: %v", statErr)
			}

			// A separate command in the same session completes normally.
			f.Runtime.Evaluate = commandprogram.Evaluate
			probe, err := f.Call(t, t.Context(), "echo session-ok")
			testutil.FailErr(t, "run subsequent command", err)
			if outcome := decodeOutcome(t, probe); outcome.Status != 0 || outcome.Stdout != "session-ok\n" {
				t.Fatalf("subsequent command outcome = %+v", outcome)
			}
		})
	}
}

// TestProgramStopDuringContinuationRegistrationViaProjectEvent stops the
// continued handle at the earliest observable point (project process "started"
// event, before spec.Run begins) and verifies both publisher and stop return
// with closed admission.
func TestProgramStopDuringContinuationRegistrationViaProjectEvent(t *testing.T) {
	completed := make(chan bgprocess.Completion, 2)
	var progCtx atomic.Value
	var stopErr atomic.Value
	var admitAfterStopErr atomic.Value
	var holdAfterStopErr atomic.Value
	var registry *bgprocess.Registry
	var projSvc *projectcontrol.Service
	var f *programtest.Fixture

	registry = bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
		PublishProject: func(ctx context.Context, ev api.ProjectBackgroundEvent) {
			if ev.Action != "started" {
				return
			}
			if _, err := projSvc.StopProjectBackgroundProcess(ctx, ev.ProjectID, ev.ProcessID); err != nil {
				stopErr.Store(err)
			}
			if ctxVal, ok := progCtx.Load().(context.Context); ok {
				if err := f.Runtime.Supervisor.AdmitChild(ctxVal); err != nil {
					admitAfterStopErr.Store(err)
				}
				if _, _, _, err := f.Runtime.Supervisor.Hold(ctxVal); err != nil {
					holdAfterStopErr.Store(err)
				}
			}
		},
	})
	registry.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })

	projSvc = projectcontrol.New(nil, nil, nil, nil, nil, nil, nil, nil)
	projSvc.SetProjects(stubProjects{byID: map[string]*project.Project{
		testdbseed.DefaultProjectID: {ID: testdbseed.DefaultProjectID, Name: "test"},
	}})
	projSvc.SetProcesses(registry)

	f = programtest.New(t, programtest.Options{Background: registry})
	clock := programtest.NewManualClock()
	f.Runtime.Clock = clock
	entered := make(chan struct{})
	f.Runtime.Evaluate = func(ctx context.Context, _ commandsurface.ProgramSnapshot, _ commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		progCtx.Store(ctx)
		close(entered)
		<-ctx.Done()
		return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
	}

	done := make(chan callResult, 1)
	go func() {
		s, e := f.Call(t, t.Context(), "early-visible-stop")
		done <- callResult{s, e}
	}()
	testutil.AwaitClosed(t, "program entered", entered)
	clock.Advance(toolcontract.ProgramInlineWait)
	got := testutil.Receive(t, "continue result", done)
	testutil.FailErr(t, "continue program", got.err)
	if errVal := stopErr.Load(); errVal != nil {
		t.Fatalf("earliest StopProjectBackgroundProcess failed: %v", errVal)
	}
	if admitAfterStopErr.Load() == nil {
		t.Fatal("AdmitChild was not refused immediately after earliest stop")
	}
	if holdAfterStopErr.Load() == nil {
		t.Fatal("Hold was not refused immediately after earliest stop")
	}
	c := testutil.Receive(t, "completion", completed)
	if c.TerminationReason != bgprocess.TerminationStoppedByUser {
		t.Fatalf("completion reason = %q, want stopped_by_user", c.TerminationReason)
	}
}

// TestProgramStopIsolationAndAccessChecks verifies two live programs in one
// session plus an unrelated session: stopping one program leaves its sibling
// and the other session intact; stopping a single & job leaves its sibling job
// intact; cross-session and cross-project stops are refused.
func TestProgramStopIsolationAndAccessChecks(t *testing.T) {
	registry := bgprocess.NewRegistry(bgprocess.Config{MaxBackground: 8}, bgprocess.Hooks{})
	registry.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })

	projSvc := projectcontrol.New(nil, nil, nil, nil, nil, nil, nil, nil)
	projSvc.SetProjects(stubProjects{byID: map[string]*project.Project{
		testdbseed.DefaultProjectID: {ID: testdbseed.DefaultProjectID, Name: "p1"},
		"other-project":             {ID: "other-project", Name: "p2"},
	}})
	projSvc.SetProcesses(registry)
	pc := &processcontrol.Service{Background: registry}

	f1 := programtest.New(t, programtest.Options{Session: "session-a", Background: registry})
	clock1 := programtest.NewManualClock()
	f1.Runtime.Clock = clock1
	f2 := programtest.New(t, programtest.Options{Session: "session-b", Background: registry})
	clock2 := programtest.NewManualClock()
	f2.Runtime.Clock = clock2

	type gate struct {
		name    string
		entered chan struct{}
		release chan struct{}
	}
	newGate := func(name string) *gate {
		return &gate{name: name, entered: make(chan struct{}), release: make(chan struct{})}
	}
	gP1, gP2, gOther := newGate("p1"), newGate("p2"), newGate("other")

	var nextGate atomic.Pointer[gate]
	evalFor := func(ctx context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		g := nextGate.Load()
		close(g.entered)
		select {
		case <-g.release:
			_, _ = fmt.Fprintf(ports.Streams.Output, "ok:%s\n", g.name)
			return commandprogram.ProgramOutcome{Status: 0}, nil
		case <-ctx.Done():
			return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
		}
	}
	f1.Runtime.Evaluate = evalFor
	f2.Runtime.Evaluate = evalFor

	startContinued := func(f *programtest.Fixture, clock *programtest.ManualClock, g *gate, cmd string) command.RunningProgramResult {
		t.Helper()
		nextGate.Store(g)
		done := make(chan callResult, 1)
		go func() {
			s, e := f.Call(t, t.Context(), cmd)
			done <- callResult{s, e}
		}()
		testutil.AwaitClosed(t, cmd+" entered", g.entered)
		clock.Advance(toolcontract.ProgramInlineWait)
		got := testutil.Receive(t, cmd+" continued", done)
		testutil.FailErr(t, cmd+" continue", got.err)
		var res command.RunningProgramResult
		testutil.FailErr(t, cmd+" decode", json.Unmarshal([]byte(got.content), &res))
		return res
	}

	p1 := startContinued(f1, clock1, gP1, "p1")
	p2 := startContinued(f1, clock1, gP2, "p2")
	other := startContinued(f2, clock2, gOther, "other")

	// Cross-session and cross-project handle authorization must reject.
	if _, err := pc.StopBackgroundProcess(f2.Session, p1.Handle); err == nil {
		t.Fatal("cross-session StopBackgroundProcess succeeded")
	}
	if _, err := projSvc.StopProjectBackgroundProcess(t.Context(), "other-project", p1.Handle); err == nil {
		t.Fatal("cross-project StopProjectBackgroundProcess succeeded")
	}

	// Stop P1 via agent control; P2 (sibling in same session) and other (unrelated session) remain running.
	_, err := runCommandStopTool(t, registry, f1.Session, p1.Handle)
	testutil.FailErr(t, "stop p1", err)
	_, stopped, err := registry.Lifecycle.Wait(t.Context(), f1.Session, p1.Handle)
	testutil.FailErr(t, "wait p1", err)
	if !stopped {
		t.Fatal("p1 did not report stopped")
	}

	for _, check := range []struct {
		sess   string
		handle string
	}{
		{f1.Session, p2.Handle},
		{f2.Session, other.Handle},
	} {
		snap, err := registry.Snapshot(t.Context(), check.sess, check.handle, 1024)
		testutil.FailErr(t, "snapshot sibling "+check.handle, err)
		if snap.HasExit {
			t.Fatalf("sibling %s unexpectedly exited when p1 stopped", check.handle)
		}
	}

	close(gP2.release)
	close(gOther.release)
	st2, stopped2, err := registry.Lifecycle.Wait(t.Context(), f1.Session, p2.Handle)
	testutil.FailErr(t, "wait p2", err)
	stOther, stoppedOther, err := registry.Lifecycle.Wait(t.Context(), f2.Session, other.Handle)
	testutil.FailErr(t, "wait other", err)
	if st2 != 0 || stopped2 || stOther != 0 || stoppedOther {
		t.Fatalf("siblings altered: p2=(%d,%t) other=(%d,%t)", st2, stopped2, stOther, stoppedOther)
	}
}

// TestProgramInterruptKeepsBackgroundJobAndStopCancelsApproval covers:
// 1. Foreground interrupt leaves an existing & job usable and independently stoppable.
// 2. Explicit program Stop while a child awaits approval releases the wait and
//    a subsequent approval cannot launch the pending operation.
func TestProgramInterruptKeepsBackgroundJobAndStopCancelsApproval(t *testing.T) {
	t.Run("foreground_interrupt_keeps_background_job", func(t *testing.T) {
		registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
		registry.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
		t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
		f := programtest.New(t, programtest.Options{Background: registry})
		clock := programtest.NewManualClock()
		f.Runtime.Clock = clock

		done := make(chan callResult, 1)
		go func() {
			s, e := f.Call(t, t.Context(), "sleep 30 & sleep 30")
			done <- callResult{s, e}
		}()

		var jobHandle string
		testutil.WaitFor(t, "background job registered", func() bool {
			jobs := registry.ActiveJobs(f.Session)
			if len(jobs) == 1 {
				jobHandle = jobs[0].Handle
				return true
			}
			return false
		})

		// Interrupt the foreground only; the background & job must survive.
		testutil.WaitFor(t, "foreground interrupted", func() bool {
			progs := f.Programs(t)
			return len(progs) > 0
		})
		// Promote to parked or interrupt directly via supervisor: use clock to stay inline and interrupt.
		// Look up the live program ID from the recorder.
		var progID string
		testutil.WaitFor(t, "program id visible", func() bool {
			// Advance clock to promote to continued handle so we get ProgramID cleanly, OR read from active programs.
			return true
		})
		clock.Advance(toolcontract.ProgramInlineWait)
		got := testutil.Receive(t, "continued program", done)
		testutil.FailErr(t, "continue program", got.err)
		var running command.RunningProgramResult
		testutil.FailErr(t, "decode running", json.Unmarshal([]byte(got.content), &running))
		progID = running.ProgramID

		if !f.Runtime.Supervisor.Interrupt(progID) {
			t.Fatal("Interrupt returned false for active foreground")
		}
		_, _, err := registry.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
		testutil.FailErr(t, "wait interrupted foreground handle", err)

		// The background & job must still be running and independently stoppable!
		snap, err := registry.Snapshot(t.Context(), f.Session, jobHandle, 1024)
		testutil.FailErr(t, "snapshot surviving job", err)
		if snap.HasExit {
			t.Fatal("foreground Interrupt stopped the background & job")
		}
		pc := &processcontrol.Service{Background: registry}
		stopRes, err := pc.StopBackgroundProcess(f.Session, jobHandle)
		testutil.FailErr(t, "independently stop surviving job", err)
		if !stopRes.StopRequested {
			t.Fatalf("independent stop failed: %+v", stopRes)
		}
		_, stopped, err := registry.Lifecycle.Wait(t.Context(), f.Session, jobHandle)
		testutil.FailErr(t, "wait stopped surviving job", err)
		if !stopped {
			t.Fatal("surviving job did not settle as stopped")
		}
	})

	t.Run("explicit_stop_releases_approval_and_blocks_launch", func(t *testing.T) {
		approval := &programApprovalPoller{entered: make(chan context.Context, 1), answer: make(chan struct{})}
		invoked, ask := make(chan struct{}), make(chan struct{})
		var launchedAfterApproval atomic.Bool
		var f *programtest.Fixture
		f = programtest.New(t, programtest.Options{Natives: func(string) map[string]command.NativeOperation {
			operation := firstLineOperation()
			prepare := operation.Prepare
			operation.Prepare = func(ctx context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := prepare(ctx, paths, dispatch, request)
				prepared.Invoke = func(ctx context.Context, _ tools.ToolContext, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
					close(invoked)
					<-ask
					if _, err := hitl.WaitForCheckpoint(ctx, approval, "approval"); err != nil {
						return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, err
					}
					if err := f.Runtime.Supervisor.AdmitChild(ctx); err != nil {
						return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, err
					}
					launchedAfterApproval.Store(true)
					return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
				}
				return prepared, err
			}
			return map[string]command.NativeOperation{"head": operation}
		}})
		clock := programtest.NewManualClock()
		f.Runtime.Clock = clock
		done := make(chan callResult, 1)
		go func() {
			s, e := f.Call(t, t.Context(), "head; echo after")
			done <- callResult{s, e}
		}()
		testutil.AwaitClosed(t, "native invoked", invoked)
		clock.Advance(toolcontract.ProgramInlineWait)
		got := testutil.Receive(t, "continued", done)
		testutil.FailErr(t, "continue", got.err)
		var running command.RunningProgramResult
		testutil.FailErr(t, "decode running", json.Unmarshal([]byte(got.content), &running))
		close(ask)
		waitCtx := testutil.Receive(t, "approval entered", approval.entered)

		_, err := runCommandStopTool(t, f.Background, f.Session, running.Handle)
		testutil.FailErr(t, "stop during approval", err)
		testutil.AwaitClosed(t, "approval wait released", waitCtx.Done())
		// Subsequent approval arrives after Stop; operation must not launch.
		close(approval.answer)
		_, stopped, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
		testutil.FailErr(t, "wait stopped program", err)
		if !stopped {
			t.Fatal("expected stopped=true")
		}
		if launchedAfterApproval.Load() {
			t.Fatal("subsequent approval launched pending operation after Stop")
		}
	})
}

// TestProgramStopExternalTreeAndNativeJob exercises a continued program with
// both a native background job and a supervised external child process tree,
// confirming native cancellation, observed OS exit of external descendants,
// released waiters, and reusable registry capacity on macOS and Linux.
func TestProgramStopExternalTreeAndNativeJob(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix process tree fixture")
	}
	for _, viaUser := range []bool{false, true} {
		name := "agent_stop"
		wantReason := bgprocess.TerminationStoppedByAgent
		if viaUser {
			name = "user_stop"
			wantReason = bgprocess.TerminationStoppedByUser
		}
		t.Run(name, func(t *testing.T) {
			completed := make(chan bgprocess.Completion, 8)
			registry := bgprocess.NewRegistry(bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{
				Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
			})
			registry.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
			t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })

			f := programtest.New(t, programtest.Options{Background: registry})
			clock := programtest.NewManualClock()
			f.Runtime.Clock = clock

			pidFile := filepath.Join(f.Dir, "descendant.pid")
			fifoPath := filepath.Join(f.Dir, "descendant.fifo")
			laterFile := filepath.Join(f.Dir, "later.marker")
			testutil.FailErr(t, "create descendant fifo", syscall.Mkfifo(fifoPath, 0o600))

			// The descendant holds the FIFO write end open for its entire lifetime.
			// EOF on the read end is kernel-enforced proof of OS exit on both macOS
			// and Linux (including inside PID-namespaced sandboxes).
			descendantReady := make(chan struct{})
			descendantExited := make(chan struct{})
			go func() {
				defer close(descendantExited)
				rd, err := os.OpenFile(fifoPath, os.O_RDONLY, 0)
				if err != nil {
					return
				}
				defer func() { _ = rd.Close() }()
				br := bufio.NewReader(rd)
				if line, err := br.ReadString('\n'); err == nil && strings.TrimSpace(line) == "ready" {
					close(descendantReady)
				}
				_, _ = io.Copy(io.Discard, br)
			}()

			script := `sleep 300 & external -- /bin/sh -c '(exec 3>descendant.fifo; echo ready >&3; sleep 300 >&3) & echo $! > descendant.pid; wait' & wait; external -- /bin/sh -c 'echo ran > later.marker'`
			done := make(chan callResult, 1)
			go func() {
				s, e := f.Call(t, t.Context(), script)
				done <- callResult{s, e}
			}()

			var descendantPID int
			testutil.WaitFor(t, "external descendant live", func() bool {
				select {
				case early := <-done:
					t.Fatalf("program exited before descendant became ready: err=%v content=%s", early.err, early.content)
				case <-descendantReady:
				default:
					return false
				}
				raw, err := os.ReadFile(pidFile)
				if err != nil {
					return false
				}
				pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
				if err != nil || pid <= 0 {
					return false
				}
				descendantPID = pid
				return true
			})

			clock.Advance(toolcontract.ProgramInlineWait)
			got := testutil.Receive(t, "continued program", done)
			testutil.FailErr(t, "continue program", got.err)
			var running command.RunningProgramResult
			testutil.FailErr(t, "decode running", json.Unmarshal([]byte(got.content), &running))

			// Collect all active job handles (continued program + native & + external &).
			activeBefore := registry.ActiveJobs(f.Session)
			if len(activeBefore) < 3 {
				t.Fatalf("expected at least 3 active handles (program + 2 jobs), got %d", len(activeBefore))
			}

			if viaUser {
				pc := &processcontrol.Service{Background: registry}
				_, err := pc.StopBackgroundProcess(f.Session, running.Handle)
				testutil.FailErr(t, "user stop program", err)
			} else {
				_, err := runCommandStopTool(t, registry, f.Session, running.Handle)
				testutil.FailErr(t, "agent command_stop program", err)
			}

			// Wait for the continued program and every held job handle to settle.
			for _, job := range activeBefore {
				_, stopped, err := registry.Lifecycle.Wait(t.Context(), f.Session, job.Handle)
				testutil.FailErr(t, "wait handle "+job.Handle, err)
				if !stopped {
					t.Fatalf("handle %s did not settle as stopped", job.Handle)
				}
				out := runCommandOutputTool(t, registry, f.Session, job.Handle)
				if out["running"] != false {
					t.Fatalf("command_output for %s still running: %+v", job.Handle, out)
				}
				if out["termination_reason"] != string(wantReason) {
					t.Fatalf("handle %s termination_reason = %v, want %q", job.Handle, out["termination_reason"], wantReason)
				}
			}

			// Confirm observed OS exit of the external descendant (FIFO EOF on all Unix
			// platforms, plus host PID check on macOS where no PID namespace remaps PIDs).
			testutil.AwaitClosed(t, "external descendant exited (FIFO EOF)", descendantExited)
			if runtime.GOOS == "darwin" {
				testutil.WaitFor(t, "external descendant PID gone", func() bool {
					return !hostprocess.ProcessExists(descendantPID)
				})
			}
			if _, statErr := os.Stat(laterFile); !os.IsNotExist(statErr) {
				t.Fatalf("later marker written after stop: %v", statErr)
			}

			// Confirm reusable registry capacity.
			testutil.WaitFor(t, "registry active jobs empty", func() bool {
				return len(registry.ActiveJobs(f.Session)) == 0
			})
			probe, err := f.Call(t, t.Context(), "echo capacity-reused")
			testutil.FailErr(t, "reuse capacity", err)
			if outcome := decodeOutcome(t, probe); outcome.Status != 0 || outcome.Stdout != "capacity-reused\n" {
				t.Fatalf("reused capacity outcome = %+v", outcome)
			}
		})
	}
}

// TestProgramStopEffectsAndCleanupSettlement verifies:
// - An earlier committed file write and its receipt survive Stop.
// - A later statement's marker is prevented.
// - Immediate acknowledgment reports running while an admitted child is held at cleanup.
// - After cleanup finishes, terminal receipt, command_output, process exit event,
//   exit code, reason, and owner-reported EffectCertainty agree.
func TestProgramStopEffectsAndCleanupSettlement(t *testing.T) {
	completed := make(chan bgprocess.Completion, 2)
	events := make(chan api.BackgroundProcessEvent, 16)
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
		Publish:  func(_ context.Context, _, _ string, ev api.BackgroundProcessEvent) { events <- ev },
	})
	registry.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })

	enteredCleanup := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var releaseOnce sync.Once
	unlockCleanup := func() { releaseOnce.Do(func() { close(releaseCleanup) }) }
	t.Cleanup(unlockCleanup)

	var recordedCertainty atomic.Value
	var committedPath string
	var headCalls atomic.Int32
	f := programtest.New(t, programtest.Options{
		Background: registry,
		Natives: func(dir string) map[string]command.NativeOperation {
			committedPath = filepath.Join(dir, "committed.txt")
			op := firstLineOperation()
			prepare := op.Prepare
			op.Prepare = func(ctx context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := prepare(ctx, paths, dispatch, request)
				prepared.Invoke = func(ctx context.Context, _ tools.ToolContext, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
					if headCalls.Add(1) == 1 {
						if err := os.WriteFile(committedPath, []byte("kept\n"), 0o644); err != nil {
							return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectUnknown}, err
						}
						return toolcontract.ProducerResult{
							Status:          0,
							Terminal:        toolcontract.ProducerCompleted,
							Complete:        true,
							EffectCertainty: toolcontract.EffectCompleted,
						}, nil
					}
					close(enteredCleanup)
					<-ctx.Done()
					<-releaseCleanup
					res := toolcontract.ProducerResult{
						Status:          130,
						Terminal:        toolcontract.ProducerInterrupted,
						EffectCertainty: toolcontract.EffectAttempted,
					}
					recordedCertainty.Store(res.EffectCertainty)
					return res, ctx.Err()
				}
				return prepared, err
			}
			return map[string]command.NativeOperation{"head": op}
		},
	})
	clock := programtest.NewManualClock()
	f.Runtime.Clock = clock

	laterPath := filepath.Join(f.Dir, "prevented.txt")
	script := `head; head; external -- /bin/sh -c 'printf "later\n" > prevented.txt'`

	done := make(chan callResult, 1)
	go func() {
		s, e := f.Call(t, t.Context(), script)
		done <- callResult{s, e}
	}()
	testutil.AwaitClosed(t, "admitted child entered", enteredCleanup)
	clock.Advance(toolcontract.ProgramInlineWait)
	got := testutil.Receive(t, "continued program", done)
	testutil.FailErr(t, "continue program", got.err)
	var running command.RunningProgramResult
	testutil.FailErr(t, "decode running", json.Unmarshal([]byte(got.content), &running))

	// Stop while admitted child is held at cleanup; immediate ack must report running=true.
	pc := &processcontrol.Service{Background: registry}
	ack, err := pc.StopBackgroundProcess(f.Session, running.Handle)
	testutil.FailErr(t, "stop while held at cleanup", err)
	if !ack.StopRequested || !ack.Running || ack.TerminationReason != api.BackgroundProcessTerminationReason(bgprocess.TerminationStoppedByUser) {
		t.Fatalf("immediate ack = %+v, want stop_requested=true running=true stopped_by_user", ack)
	}

	// Release cleanup and verify settlement across receipt, command_output, and exit event.
	unlockCleanup()
	exitCode, stopped, err := registry.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
	testutil.FailErr(t, "wait settled program", err)
	if !stopped || exitCode != 130 {
		t.Fatalf("settled wait exit=%d stopped=%t", exitCode, stopped)
	}

	c := testutil.Receive(t, "completion", completed)
	if c.TerminationReason != bgprocess.TerminationStoppedByUser || c.ExitCode != 130 {
		t.Fatalf("completion reason=%q exit=%d", c.TerminationReason, c.ExitCode)
	}

	out := runCommandOutputTool(t, registry, f.Session, running.Handle)
	if out["running"] != false || out["termination_reason"] != string(bgprocess.TerminationStoppedByUser) {
		t.Fatalf("command_output = %+v", out)
	}

	var sawExitEvent bool
	for len(events) > 0 {
		ev := <-events
		if ev.Stream == "exit" && ev.ProcessID == running.Handle {
			sawExitEvent = true
			if ev.Running || ev.ExitCode == nil || *ev.ExitCode != 130 || ev.TerminationReason != api.BackgroundProcessTerminationReason(bgprocess.TerminationStoppedByUser) {
				t.Fatalf("exit event = %+v", ev)
			}
		}
	}
	if !sawExitEvent {
		t.Fatal("missing terminal process exit event")
	}

	// Earlier committed file write survived; later marker was prevented.
	data, err := os.ReadFile(committedPath)
	testutil.FailErr(t, "read committed file", err)
	if string(data) != "kept\n" {
		t.Fatalf("committed file = %q, want kept\\n", string(data))
	}
	if _, statErr := os.Stat(laterPath); !os.IsNotExist(statErr) {
		t.Fatalf("later statement marker existed: %v", statErr)
	}

	// Terminal receipt reflects interrupted state and preserves uncertain/attempted effects.
	projection, err := f.Recorder.ProjectProgram(t.Context(), f.Session, running.ProgramID, 1)
	testutil.FailErr(t, "project settled program", err)
	if projection.Program.State != "interrupted" {
		t.Fatalf("program receipt state = %q, want interrupted", projection.Program.State)
	}
	if projection.CompletedEffects < 1 {
		t.Fatalf("expected earlier committed effect receipt to survive, got CompletedEffects=%d", projection.CompletedEffects)
	}
	if projection.UncertainEffects < 1 {
		t.Fatalf("expected partially completed effect uncertainty to survive in receipt, got UncertainEffects=%d", projection.UncertainEffects)
	}
	if recordedCertainty.Load() != toolcontract.EffectAttempted {
		t.Fatalf("owner certainty = %v, want EffectAttempted", recordedCertainty.Load())
	}
}
