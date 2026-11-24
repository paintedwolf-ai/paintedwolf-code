package loopwake

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func TestUntilCompleteParksReleasesTurnAndDeliversOnce(t *testing.T) {
	clock := programtest.NewManualClock()
	f := newEntryFixture(t, programtest.NewWaitingProgram(clock, programtest.SleepState(0)))
	entered, release := make(chan struct{}), make(chan struct{})
	var runs atomic.Int32
	f.Runtime.Evaluate = func(_ context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		runs.Add(1)
		_, _ = fmt.Fprint(ports.Streams.Output, "part1\n")
		close(entered)
		<-release
		_, _ = fmt.Fprint(ports.Streams.Output, "part2\n")
		return commandprogram.ProgramOutcome{Status: 0}, nil
	}

	done := make(chan string, 1)
	go func() {
		content, err := f.CallArgs(t, t.Context(), map[string]any{"command": "step1; step2", "until_complete": true})
		testutil.FailErr(t, "until_complete call", err)
		done <- content
	}()
	<-entered
	clock.Advance(toolcontract.ProgramInlineWait)
	content := <-done

	var parked command.ParkedProgramResult
	testutil.FailErr(t, "decode parked result", json.Unmarshal([]byte(content), &parked))
	if parked.State != "parked" || !ParkedCompletionEndsCycle(f.out.Completion) {
		t.Fatalf("unexpected parked result %s completion=%+v", content, f.out.Completion)
	}
	if f.loop.Waits.OnTurnComplete(t.Context(), f.Session, false) != UserTurnContinues {
		t.Fatal("parked command retained its turn")
	}
	// Zero intermediate wakes before the program finishes.
	select {
	case delivery := <-f.resumed:
		t.Fatalf("unexpected intermediate wake: %+v", delivery)
	default:
	}

	close(release)
	delivery := <-f.resumed
	outcome := decodeProgram(t, delivery.Condition.Report)
	if delivery.LeaseID != parked.LeaseID || delivery.Condition.Outcome != "completed" ||
		outcome.ProgramID != parked.ProgramID || outcome.Status != 0 || outcome.Stdout != "part1\npart2\n" {
		t.Fatalf("unexpected delivery %+v outcome %+v", delivery, outcome)
	}
	select {
	case extra := <-f.resumed:
		t.Fatalf("unexpected second delivery: %+v", extra)
	default:
	}
	if runs.Load() != 1 {
		t.Fatalf("runs=%d, want 1", runs.Load())
	}
}

func TestUntilCompleteReloadDuringSettlementDeliversSettledResult(t *testing.T) {
	clock := programtest.NewManualClock()
	f := newEntryFixture(t, programtest.NewWaitingProgram(clock, programtest.SleepState(0)))
	// Hold resume delivery so settlement persists before delivery is acknowledged.
	f.loop.Deliveries.setDeps(WaitDeliveriesDeps{})
	entered, release := make(chan struct{}), make(chan struct{})
	f.Runtime.Evaluate = func(_ context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		close(entered)
		<-release
		_, _ = fmt.Fprint(ports.Streams.Output, "settled\n")
		return commandprogram.ProgramOutcome{Status: 0}, nil
	}

	done := make(chan string, 1)
	go func() {
		content, err := f.CallArgs(t, t.Context(), map[string]any{"command": "work", "until_complete": true})
		testutil.FailErr(t, "until_complete call", err)
		done <- content
	}()
	<-entered
	clock.Advance(toolcontract.ProgramInlineWait)
	content := <-done
	var parked command.ParkedProgramResult
	testutil.FailErr(t, "decode parked result", json.Unmarshal([]byte(content), &parked))

	close(release)
	testutil.WaitFor(t, "settled lease persists", func() bool {
		lease, ok, err := f.store.LatestResumeCandidate(t.Context(), f.Session)
		return err == nil && ok && lease.ID == parked.LeaseID && lease.Status == "resolved"
	})

	restarted := f.engine(t)
	restarted.Subscriptions.SetLostProgramResult(f.Runtime.ReceiptResult)
	testutil.FailErr(t, "recover wait leases", RecoverWaitLeases(t.Context(), restarted.Subscriptions, f.store))
	delivery := <-f.resumed
	outcome := decodeProgram(t, delivery.Condition.Report)
	if delivery.LeaseID != parked.LeaseID || delivery.Condition.Outcome != "completed" || outcome.Stdout != "settled\n" {
		t.Fatalf("recovered settled delivery %+v outcome %+v", delivery, outcome)
	}
}

func TestUntilCompleteStopWhileParkedDeliversInterruptedOnce(t *testing.T) {
	clock := programtest.NewManualClock()
	f := newEntryFixture(t, programtest.NewWaitingProgram(clock, programtest.SleepState(0)))
	entered := make(chan struct{})
	f.Runtime.Evaluate = func(ctx context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		_, _ = fmt.Fprint(ports.Streams.Output, "started\n")
		close(entered)
		<-ctx.Done()
		return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
	}

	done := make(chan string, 1)
	go func() {
		content, err := f.CallArgs(t, t.Context(), map[string]any{"command": "long-op", "until_complete": true})
		testutil.FailErr(t, "until_complete call", err)
		done <- content
	}()
	<-entered
	clock.Advance(toolcontract.ProgramInlineWait)
	<-done

	f.loop.Waits.InterruptSleep(t.Context(), f.Session)
	ended := f.loop.Deliveries.TakeEndedWaits(f.Session)
	if len(ended) != 1 || ended[0].Outcome != "interrupted" || ended[0].Code != toolrejection.ToolOwnerInterruptedCode {
		t.Fatalf("ended waits %+v", ended)
	}
	outcome := decodeProgram(t, ended[0].Report)
	if outcome.State != "interrupted" || outcome.Status != 130 || outcome.Stdout != "started\n" {
		t.Fatalf("unexpected interrupted outcome %+v", outcome)
	}
	if again := f.loop.Deliveries.TakeEndedWaits(f.Session); len(again) != 0 {
		t.Fatalf("interrupted outcome delivered twice: %+v", again)
	}
}
