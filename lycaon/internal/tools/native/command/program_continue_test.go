package command_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func TestProgramContinuationPreservesSequenceAndOutput(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint("stop=", stop), func(t *testing.T) {
			f := programtest.New(t, programtest.Options{})
			clock := programtest.NewManualClock()
			f.Runtime.Clock = clock
			out := &tools.ToolInvocationOut{}
			f.Configure = func(tc *tools.ToolContext) { tc.Effects.Out = out }
			runs := &recordedRuns{}
			f.Runtime.SourceRuns = runs
			entered, release, later := make(chan struct{}), make(chan struct{}), make(chan struct{})
			f.Runtime.Evaluate = func(ctx context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
				fmt.Fprint(ports.Streams.Output, "listening\n")
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
				}
				close(later)
				fmt.Fprint(ports.Streams.Output, "later statement\n")
				return commandprogram.ProgramOutcome{Status: 7}, nil
			}
			caller, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan callResult, 1)
			go func() {
				s, e := f.CallArgs(t, caller, map[string]any{"command": "server; later"})
				done <- callResult{s, e}
			}()
			<-entered
			clock.Advance(command.DefaultProgramInlineWait - time.Second)
			select {
			case got := <-done:
				t.Fatalf("returned early: %+v", got)
			default:
			}
			clock.Advance(time.Second)
			got := <-done
			testutil.FailErr(t, "continue program", got.err)
			var running command.RunningProgramResult
			testutil.FailErr(t, "decode handle", json.Unmarshal([]byte(got.content), &running))
			if running.State != "running" || running.Handle == "" {
				t.Fatalf("result %+v", running)
			}
			if out.SourceRun != nil || len(runs.recorded()) != 0 || out.CommandProgram == nil || out.CommandProgram.State != "running" {
				t.Fatalf("running program claimed completion: %+v", out)
			}
			if _, err := f.Background.Snapshot(t.Context(), "another-session", running.Handle, 4096); err == nil {
				t.Fatal("another session inspected the handle")
			}
			cancel()
			select {
			case <-later:
				t.Fatal("continuation ran the later statement early")
			default:
			}
			snap, err := f.Background.Snapshot(t.Context(), f.Session, running.Handle, 4096)
			testutil.FailErr(t, "inspect live output", err)
			if snap.HasExit || snap.Output != "stdout: listening\n" {
				t.Fatalf("live snapshot %+v", snap)
			}
			if stop {
				_, err = f.Background.Lifecycle.Stop(f.Session, running.Handle)
				testutil.FailErr(t, "stop original program", err)
			} else {
				close(release)
			}
			status, _, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
			testutil.FailErr(t, "join continuation", err)
			if !stop && status != 7 {
				t.Fatalf("status %d", status)
			}
			snap, err = f.Background.Snapshot(t.Context(), f.Session, running.Handle, 4096)
			testutil.FailErr(t, "inspect completed output", err)
			if !stop && !strings.HasPrefix(snap.Output, "stdout: listening\nstdout: later statement\nstderr: {") {
				t.Fatalf("output %q", snap.Output)
			}
			marker := strings.LastIndex(snap.Output, "stderr: {")
			if marker < 0 {
				t.Fatalf("missing final outcome: %s", snap.Output)
			}
			var final command.ProgramOutcome
			testutil.FailErr(t, "read final outcome", json.Unmarshal([]byte(snap.Output[marker+len("stderr: "):]), &final))
			if final.ProgramID != running.ProgramID || (!stop && final.Status != 7) {
				t.Fatalf("final outcome %+v", final)
			}
			if out.SourceRun != nil || out.CommandProgram.State != "running" {
				t.Fatal("completion changed returned call effects")
			}
			if len(runs.recorded()) != 1 {
				t.Fatalf("completion recorded %d runs", len(runs.recorded()))
			}
			if stop {
				select {
				case <-later:
					t.Fatal("stop ran later statement")
				default:
				}
			}
			if clock.Pending() != 0 {
				t.Fatalf("retained %d timers", clock.Pending())
			}
		})
	}
}

func TestProgramContinuationRegistrationFailureInterruptsOriginal(t *testing.T) {
	f := programtest.New(t, programtest.Options{})
	clock := programtest.NewManualClock()
	f.Runtime.Clock = clock
	entered, interrupted := make(chan struct{}), make(chan struct{})
	f.Runtime.Evaluate = func(ctx context.Context, _ commandsurface.ProgramSnapshot, _ commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		close(entered)
		<-ctx.Done()
		close(interrupted)
		return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
	}
	testutil.FailErr(t, "close continuation registry", f.Background.Lifecycle.Close(t.Context()))
	done := make(chan callResult, 1)
	go func() {
		s, e := f.CallArgs(t, t.Context(), map[string]any{"command": "server"})
		done <- callResult{s, e}
	}()
	<-entered
	clock.Advance(command.DefaultProgramInlineWait)
	got := <-done
	if got.err == nil {
		t.Fatal("closed registry returned a running handle")
	}
	<-interrupted
	if clock.Pending() != 0 {
		t.Fatalf("retained %d timers", clock.Pending())
	}
}

func TestProgramContinuationCallerCancellationStaysInline(t *testing.T) {
	f := programtest.New(t, programtest.Options{})
	clock := programtest.NewManualClock()
	f.Runtime.Clock = clock
	entered := make(chan struct{})
	f.Runtime.Evaluate = func(ctx context.Context, _ commandsurface.ProgramSnapshot, _ commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		close(entered)
		<-ctx.Done()
		return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
	}
	caller, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan callResult, 1)
	go func() { s, e := f.CallArgs(t, caller, map[string]any{"command": "server"}); done <- callResult{s, e} }()
	<-entered
	cancel()
	clock.Advance(command.DefaultProgramInlineWait)
	got := <-done
	if strings.Contains(got.content, `"handle"`) {
		t.Fatalf("canceled call promoted: %s", got.content)
	}
	if len(f.Background.ActiveJobs(f.Session)) != 0 {
		t.Fatal("canceled call left a background job")
	}
	if clock.Pending() != 0 {
		t.Fatalf("retained %d timers", clock.Pending())
	}
}
