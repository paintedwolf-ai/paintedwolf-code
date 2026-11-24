package command_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func TestProgramCompletionDisposition(t *testing.T) {
	t.Run("inline success with until_complete", func(t *testing.T) {
		clock := programtest.NewManualClock()
		parking := newRecordingParking()
		f := programtest.New(t, programtest.Options{})
		f.Runtime.Clock, f.Runtime.Parking = clock, parking
		var runs atomic.Int32
		f.Runtime.Evaluate = func(_ context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
			runs.Add(1)
			_, _ = fmt.Fprint(ports.Streams.Output, "fast\n")
			return commandprogram.ProgramOutcome{Status: 0}, nil
		}

		content, err := f.CallArgs(t, t.Context(), map[string]any{"command": "echo fast", "until_complete": true})
		testutil.FailErr(t, "inline until_complete", err)
		if outcome := decodeOutcome(t, content); outcome.State != "completed" || outcome.Status != 0 || outcome.Stdout != "fast\n" {
			t.Fatalf("unexpected inline outcome: %+v", outcome)
		}
		if runs.Load() != 1 || parking.count() != 0 {
			t.Fatalf("runs=%d parks=%d, want 1 and 0", runs.Load(), parking.count())
		}
	})

	t.Run("expiry with until_complete parks multi-statement program and settles once", func(t *testing.T) {
		clock := programtest.NewManualClock()
		parking := newRecordingParking()
		f := programtest.New(t, programtest.Options{})
		f.Runtime.Clock, f.Runtime.Parking = clock, parking
		var out *tools.ToolInvocationOut
		f.Configure = func(tc *tools.ToolContext) {
			out = &tools.ToolInvocationOut{}
			tc.Effects.Out = out
		}
		entered, release := make(chan struct{}), make(chan struct{})
		var runs atomic.Int32
		f.Runtime.Evaluate = func(_ context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
			runs.Add(1)
			_, _ = fmt.Fprint(ports.Streams.Output, "stmt1\n")
			close(entered)
			<-release
			_, _ = fmt.Fprint(ports.Streams.Output, "stmt2\n")
			_, _ = fmt.Fprint(ports.Streams.Error, "warn\n")
			return commandprogram.ProgramOutcome{Status: 0}, nil
		}

		done := make(chan callResult, 1)
		go func() {
			s, e := f.CallArgs(t, t.Context(), map[string]any{"command": "stmt1; stmt2", "until_complete": true})
			done <- callResult{s, e}
		}()
		<-entered
		clock.Advance(toolcontract.ProgramInlineWait)
		res := <-done
		testutil.FailErr(t, "parked until_complete", res.err)

		var parked command.ParkedProgramResult
		testutil.FailErr(t, "decode parked result", json.Unmarshal([]byte(res.content), &parked))
		if parked.State != "parked" || parked.WakeAt != "" {
			t.Fatalf("unexpected parked result %s", res.content)
		}
		rec := <-parking.parked
		close(release)
		settled := <-rec.settled
		testutil.FailErr(t, "settled error", settled.err)
		outcome := decodeOutcome(t, settled.result)
		if outcome.ProgramID != parked.ProgramID || outcome.State != "completed" || outcome.Status != 0 || outcome.Stdout != "stmt1\nstmt2\n" || outcome.Stderr != "warn\n" {
			t.Fatalf("unexpected settled outcome %+v", outcome)
		}
		if runs.Load() != 1 || parking.count() != 1 {
			t.Fatalf("runs=%d parks=%d, want 1 and 1", runs.Load(), parking.count())
		}
	})

	t.Run("omission returns running handle on expiry", func(t *testing.T) {
		registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
		registry.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
		t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })

		clock := programtest.NewManualClock()
		parking := newRecordingParking()
		f := programtest.New(t, programtest.Options{Background: registry})
		f.Runtime.Clock, f.Runtime.Parking = clock, parking
		entered, release := make(chan struct{}), make(chan struct{})
		f.Runtime.Evaluate = func(ctx context.Context, _ commandsurface.ProgramSnapshot, _ commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
			close(entered)
			select {
			case <-release:
				return commandprogram.ProgramOutcome{Status: 0}, nil
			case <-ctx.Done():
				return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
			}
		}

		done := make(chan callResult, 1)
		go func() {
			s, e := f.CallArgs(t, t.Context(), map[string]any{"command": "slow-work"})
			done <- callResult{s, e}
		}()
		<-entered
		clock.Advance(toolcontract.ProgramInlineWait)
		res := <-done
		testutil.FailErr(t, "handle return", res.err)
		var running command.RunningProgramResult
		testutil.FailErr(t, "decode running result", json.Unmarshal([]byte(res.content), &running))
		if running.State != "running" || running.Handle == "" || parking.count() != 0 {
			t.Fatalf("unexpected running result %+v parks=%d", running, parking.count())
		}
		close(release)
		_, _, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
		testutil.FailErr(t, "wait running handle", err)
	})

	t.Run("non-zero failure and refusal after expiry", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			status      int
			evalErr     error
			wantRefusal bool
		}{
			{name: "failure", status: 2},
			{name: "refusal", status: 1, evalErr: &toolrejection.ToolReject{Code: "COMMAND_OPERATION_NOT_ALLOWED"}, wantRefusal: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				clock := programtest.NewManualClock()
				parking := newRecordingParking()
				f := programtest.New(t, programtest.Options{})
				f.Runtime.Clock, f.Runtime.Parking = clock, parking
				entered, release := make(chan struct{}), make(chan struct{})
				f.Runtime.Evaluate = func(_ context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
					close(entered)
					<-release
					_, _ = fmt.Fprint(ports.Streams.Error, "failed\n")
					return commandprogram.ProgramOutcome{Status: tc.status}, tc.evalErr
				}
				done := make(chan callResult, 1)
				go func() {
					s, e := f.CallArgs(t, t.Context(), map[string]any{"command": "failing", "until_complete": true})
					done <- callResult{s, e}
				}()
				<-entered
				clock.Advance(toolcontract.ProgramInlineWait)
				res := <-done
				testutil.FailErr(t, "parked call", res.err)
				rec := <-parking.parked
				close(release)
				settled := <-rec.settled
				testutil.FailErr(t, "settled error", settled.err)
				outcome := decodeOutcome(t, settled.result)
				if outcome.Stderr != "failed\n" || outcome.Status != tc.status {
					t.Fatalf("unexpected outcome %+v", outcome)
				}
				if tc.wantRefusal && (outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_OPERATION_NOT_ALLOWED") {
					t.Fatalf("expected refusal outcome, got %+v", outcome)
				}
			})
		}
	})

	t.Run("human approval pauses foreground budget before parking", func(t *testing.T) {
		clock := programtest.NewManualClock()
		parking := newRecordingParking()
		approval := &programApprovalPoller{entered: make(chan context.Context, 1), answer: make(chan struct{})}
		f := programtest.New(t, programtest.Options{})
		f.Runtime.Clock, f.Runtime.Parking = clock, parking
		afterApproval, release := make(chan struct{}), make(chan struct{})
		f.Runtime.Evaluate = func(ctx context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
			if _, err := hitl.WaitForCheckpoint(ctx, approval, "approval"); err != nil {
				return commandprogram.ProgramOutcome{Status: 130}, err
			}
			close(afterApproval)
			<-release
			_, _ = fmt.Fprint(ports.Streams.Output, "approved\n")
			return commandprogram.ProgramOutcome{Status: 0}, nil
		}

		done := make(chan callResult, 1)
		go func() {
			s, e := f.CallArgs(t, t.Context(), map[string]any{"command": "needs-approval", "until_complete": true})
			done <- callResult{s, e}
		}()
		<-approval.entered
		clock.Advance(50 * time.Second)
		close(approval.answer)
		<-afterApproval
		// Active time after approval is still within ProgramInlineWait, so not parked yet.
		clock.Advance(toolcontract.ProgramInlineWait - time.Second)
		select {
		case res := <-done:
			t.Fatalf("parked before active budget expired: %+v", res)
		default:
		}
		clock.Advance(time.Second)
		res := <-done
		testutil.FailErr(t, "parked after approval", res.err)
		rec := <-parking.parked
		close(release)
		settled := <-rec.settled
		testutil.FailErr(t, "settled error", settled.err)
		if outcome := decodeOutcome(t, settled.result); outcome.Stdout != "approved\n" || outcome.Status != 0 {
			t.Fatalf("unexpected outcome %+v", outcome)
		}
	})

	t.Run("concurrent completion racing park returns inline", func(t *testing.T) {
		clock := programtest.NewManualClock()
		parking := newRecordingParking()
		parking.gate = make(chan struct{})
		f := programtest.New(t, programtest.Options{})
		f.Runtime.Clock, f.Runtime.Parking = clock, parking
		entered, release := make(chan struct{}), make(chan struct{})
		var runs atomic.Int32
		f.Runtime.Evaluate = func(_ context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
			runs.Add(1)
			close(entered)
			<-release
			_, _ = fmt.Fprint(ports.Streams.Output, "raced\n")
			return commandprogram.ProgramOutcome{Status: 0}, nil
		}

		done := make(chan callResult, 1)
		go func() {
			s, e := f.CallArgs(t, t.Context(), map[string]any{"command": "race", "until_complete": true})
			done <- callResult{s, e}
		}()
		<-entered
		clock.Advance(toolcontract.ProgramInlineWait)
		// Complete the program before ParkProgram claims it.
		close(release)
		// Wait until finish() has settled the program, then open the park gate.
		time.Sleep(10 * time.Millisecond)
		close(parking.gate)
		res := <-done
		testutil.FailErr(t, "raced inline result", res.err)
		if outcome := decodeOutcome(t, res.content); outcome.State != "completed" || outcome.Stdout != "raced\n" {
			t.Fatalf("expected inline outcome, got %s", res.content)
		}
		if runs.Load() != 1 {
			t.Fatalf("runs=%d, want 1", runs.Load())
		}
	})

	t.Run("stop while parked interrupts and delivers interrupted outcome", func(t *testing.T) {
		clock := programtest.NewManualClock()
		parking := newRecordingParking()
		f := programtest.New(t, programtest.Options{})
		f.Runtime.Clock, f.Runtime.Parking = clock, parking
		entered := make(chan struct{})
		f.Runtime.Evaluate = func(ctx context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
			_, _ = fmt.Fprint(ports.Streams.Output, "partial\n")
			close(entered)
			<-ctx.Done()
			return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
		}

		done := make(chan callResult, 1)
		go func() {
			s, e := f.CallArgs(t, t.Context(), map[string]any{"command": "long-job", "until_complete": true})
			done <- callResult{s, e}
		}()
		<-entered
		clock.Advance(toolcontract.ProgramInlineWait)
		res := <-done
		testutil.FailErr(t, "parked call", res.err)
		rec := <-parking.parked
		interrupted := rec.park.Interrupt(t.Context())
		outcome := decodeOutcome(t, interrupted)
		if outcome.State != "interrupted" || outcome.Status != 130 || outcome.Stdout != "partial\n" {
			t.Fatalf("unexpected interrupted outcome %+v", outcome)
		}
	})

	t.Run("non-parking session falls back to running handle", func(t *testing.T) {
		registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
		registry.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
		t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })

		clock := programtest.NewManualClock()
		parking := newRecordingParking()
		parking.unavailable = true
		f := programtest.New(t, programtest.Options{Background: registry})
		f.Runtime.Clock, f.Runtime.Parking = clock, parking
		entered, release := make(chan struct{}), make(chan struct{})
		f.Runtime.Evaluate = func(ctx context.Context, _ commandsurface.ProgramSnapshot, _ commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
			close(entered)
			select {
			case <-release:
				return commandprogram.ProgramOutcome{Status: 0}, nil
			case <-ctx.Done():
				return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
			}
		}

		done := make(chan callResult, 1)
		go func() {
			s, e := f.CallArgs(t, t.Context(), map[string]any{"command": "fallback", "until_complete": true})
			done <- callResult{s, e}
		}()
		<-entered
		clock.Advance(toolcontract.ProgramInlineWait)
		res := <-done
		testutil.FailErr(t, "fallback handle", res.err)
		var running command.RunningProgramResult
		testutil.FailErr(t, "decode running result", json.Unmarshal([]byte(res.content), &running))
		if running.State != "running" || running.Handle == "" {
			t.Fatalf("expected running handle fallback, got %+v", running)
		}
		close(release)
		_, _, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
		testutil.FailErr(t, "wait fallback handle", err)
	})

	t.Run("separate background service retains independent lifetime", func(t *testing.T) {
		registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
		registry.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
		t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })

		clock := programtest.NewManualClock()
		parking := newRecordingParking()
		f := programtest.New(t, programtest.Options{Background: registry})
		f.Runtime.Clock, f.Runtime.Parking = clock, parking
		f.Runtime.Evaluate = commandprogram.Evaluate

		content, err := f.CallArgs(t, t.Context(), map[string]any{"command": "sleep 120 & echo started", "until_complete": true})
		testutil.FailErr(t, "background service call", err)
		outcome := decodeOutcome(t, content)
		if outcome.State != "running" || outcome.Status != 0 || outcome.Stdout != "started\n" {
			t.Fatalf("expected inline completion with background service running, got %+v", outcome)
		}
		if len(f.Background.ActiveJobs(f.Session)) == 0 {
			t.Fatal("expected background service job to remain active")
		}
	})
}
