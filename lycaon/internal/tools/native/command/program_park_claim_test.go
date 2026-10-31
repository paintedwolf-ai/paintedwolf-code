package command_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// endedWaitProgram reports a long wait and ends it at once, then runs a later
// statement until the test releases it. With second set, it then reports a
// second long wait, which lasts until the host interrupts it or the test ends it.
type endedWaitProgram struct {
	clock   *programtest.ManualClock
	second  bool
	holding chan struct{}
	release chan struct{}
	end     chan struct{}
}

func newEndedWaitProgram(second bool) *endedWaitProgram {
	return &endedWaitProgram{clock: programtest.NewManualClock(), second: second, holding: make(chan struct{}), release: make(chan struct{}), end: make(chan struct{})}
}

func (p *endedWaitProgram) evaluate(ctx context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
	_, _ = fmt.Fprint(ports.Streams.Output, "start\n")
	unblock, err := ports.Waits.Block(ctx, programtest.SleepState(120*time.Second)(p.clock.Now()))
	if err != nil {
		return commandprogram.ProgramOutcome{Status: 1}, err
	}
	unblock()
	close(p.holding)
	<-p.release
	if p.second {
		unblock, err := ports.Waits.Block(ctx, programtest.SleepState(300*time.Second)(p.clock.Now()))
		if err != nil {
			return commandprogram.ProgramOutcome{Status: 1}, err
		}
		defer unblock()
		select {
		case <-p.end:
		case <-ctx.Done():
			return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
		}
	}
	_, _ = fmt.Fprint(ports.Streams.Output, "done\n")
	return commandprogram.ProgramOutcome{}, nil
}

func newEndedWaitFixture(t *testing.T, program *endedWaitProgram) *parkFixture {
	t.Helper()
	f := &parkFixture{Fixture: programtest.New(t, programtest.Options{}), clock: program.clock, parking: newRecordingParking()}
	f.Runtime.Evaluate, f.Runtime.Clock, f.Runtime.Parking = program.evaluate, program.clock, f.parking
	f.parking.gate = program.holding
	f.Configure = func(tc *tools.ToolContext) {
		f.out = &tools.ToolInvocationOut{}
		tc.Effects.Out = f.out
	}
	return f
}

// A wait that ended before its park signal was taken never parks the call:
// the call stays live while later statements run and completes inline.
func TestProgramParkIgnoresAWaitThatEndedBeforeItParked(t *testing.T) {
	program := newEndedWaitProgram(false)
	f := newEndedWaitFixture(t, program)
	done := f.start(t, map[string]any{"command": "sleep 120; later"})
	<-f.parking.refused
	select {
	case result := <-done:
		t.Fatalf("the call ended while its program still ran: %+v", result)
	default:
	}
	close(program.release)
	result := <-done
	testutil.FailErr(t, "run program past its ended wait", result.err)
	if outcome := decodeOutcome(t, result.content); outcome.State != "completed" || outcome.Stdout != "start\ndone\n" {
		t.Fatalf("inline outcome %s", result.content)
	}
	f.parking.mu.Lock()
	defer f.parking.mu.Unlock()
	if len(f.parking.claims) != 1 || f.parking.claims[0] || f.out.Completion != nil {
		t.Fatalf("claims %v completion %+v", f.parking.claims, f.out.Completion)
	}
}

// After a stale signal the call keeps watching: a later long wait parks it.
func TestProgramParkParksALaterWaitAfterAStaleSignal(t *testing.T) {
	program := newEndedWaitProgram(true)
	f := newEndedWaitFixture(t, program)
	done := f.start(t, map[string]any{"command": "sleep 120; later; sleep 300"})
	<-f.parking.refused
	close(program.release)
	result := <-done
	testutil.FailErr(t, "park the later wait", result.err)
	var parked command.ParkedProgramResult
	testutil.FailErr(t, "decode parked result", json.Unmarshal([]byte(result.content), &parked))
	if parked.State != "parked" || parked.Reason != "sleep 300" {
		t.Fatalf("parked result %s", result.content)
	}
	recorded := <-f.parking.parked
	close(program.end)
	if outcome := f.settledProgram(t, recorded); outcome.Stdout != "start\ndone\n" || outcome.State != "completed" {
		t.Fatalf("later wait settled %+v", outcome)
	}
	f.parking.mu.Lock()
	defer f.parking.mu.Unlock()
	if len(f.parking.claims) != 2 || f.parking.claims[0] || !f.parking.claims[1] {
		t.Fatalf("claims %v", f.parking.claims)
	}
}
