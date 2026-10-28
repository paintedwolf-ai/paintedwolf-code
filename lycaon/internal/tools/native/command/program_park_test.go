package command_test

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

// recordingParking stands in for the session's wait lease.
type recordingParking struct {
	mu          sync.Mutex
	unavailable bool
	// gate, when set, holds each park until the test opens it, then the park
	// claims the wait as the host's park does.
	gate    chan struct{}
	parks   []tools.ProgramPark
	claims  []bool
	parked  chan *recordedPark
	refused chan struct{}
}

type recordedPark struct {
	park    tools.ProgramPark
	settled chan settledProgram
}

type settledProgram struct {
	result string
	err    error
}

func newRecordingParking() *recordingParking {
	return &recordingParking{parked: make(chan *recordedPark, 1), refused: make(chan struct{}, 2)}
}

func (p *recordingParking) ParkProgram(_ context.Context, _ tools.ToolContext, park tools.ProgramPark) (tools.ParkedProgram, error) {
	if p.gate != nil {
		<-p.gate
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.parks = append(p.parks, park)
	if p.unavailable {
		p.refused <- struct{}{}
		return nil, tools.ErrParkUnavailable
	}
	claimed := park.Claim()
	p.claims = append(p.claims, claimed)
	if !claimed {
		p.refused <- struct{}{}
		return nil, tools.ErrWaitEnded
	}
	parked := &recordedPark{park: park, settled: make(chan settledProgram, 1)}
	p.parked <- parked
	return parked, nil
}

func (p *recordingParking) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.parks)
}

func (p *recordedPark) LeaseID() string { return "lease-" + p.park.ProgramID }
func (p *recordedPark) Settle(_ context.Context, result string, err error) {
	p.settled <- settledProgram{result: result, err: err}
}

type recordedRuns struct {
	mu   sync.Mutex
	runs []tools.SourceRunCapture
}

func (r *recordedRuns) RecordSourceRunEvidence(_ context.Context, _ string, _ *api.Session, tool string, run tools.SourceRunCapture) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if tool == "command" {
		r.runs = append(r.runs, run)
	}
}

func (r *recordedRuns) recorded() []tools.SourceRunCapture {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tools.SourceRunCapture(nil), r.runs...)
}

type parkFixture struct {
	*programtest.Fixture
	clock   *programtest.ManualClock
	parking *recordingParking
	out     *tools.ToolInvocationOut
}

func newParkFixture(t *testing.T, program *programtest.WaitingProgram) *parkFixture {
	t.Helper()
	f := &parkFixture{Fixture: programtest.New(t, programtest.Options{}), clock: program.Clock, parking: newRecordingParking()}
	f.Runtime.Evaluate, f.Runtime.Clock, f.Runtime.Parking = program.Evaluate, program.Clock, f.parking
	f.Configure = func(tc *tools.ToolContext) {
		f.out = &tools.ToolInvocationOut{}
		tc.Effects.Out = f.out
	}
	return f
}

type callResult struct {
	content string
	err     error
}

func (f *parkFixture) start(t *testing.T, args map[string]any) chan callResult {
	t.Helper()
	done := make(chan callResult, 1)
	go func() {
		content, err := f.CallArgs(t, t.Context(), args)
		done <- callResult{content, err}
	}()
	return done
}

func (f *parkFixture) settledProgram(t *testing.T, parked *recordedPark) command.ProgramOutcome {
	t.Helper()
	settled := <-parked.settled
	testutil.FailErr(t, "settle parked program", settled.err)
	return decodeOutcome(t, settled.result)
}

// The bound a single wait must end within: the call's timeout_ms, or the
// catalog's program lifetime when the call names none.
func TestProgramParkRefusesAWaitPastTheCallBound(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     map[string]any
		wait     time.Duration
		limit    string
		maximum  int64
		observed int64
	}{
		{name: "call timeout", args: map[string]any{"command": "sleep 120", "timeout_ms": 60000}, wait: 120 * time.Second, limit: "timeout_ms", maximum: 60000, observed: 120000},
		{name: "default envelope", args: map[string]any{"command": "sleep 7200"}, wait: 7200 * time.Second, limit: "program_lifetime_ms", maximum: 3600000, observed: 7200000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newParkFixture(t, programtest.NewWaitingProgram(programtest.NewManualClock(), programtest.SleepState(tc.wait)))
			content, err := f.CallArgs(t, t.Context(), tc.args)
			testutil.FailErr(t, "run refused wait", err)
			outcome := decodeOutcome(t, content)
			if outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_LIMIT_EXCEEDED" || outcome.Stdout != "start\n" {
				t.Fatalf("refused wait outcome %s", content)
			}
			details := outcome.Refusal.Details
			maximum, _ := details["maximum"].(float64)
			observed, _ := details["observed"].(float64)
			if details["limit_id"] != tc.limit || details["resource_unit"] != "milliseconds" || int64(maximum) != tc.maximum || int64(observed) != tc.observed {
				t.Fatalf("refusal details %v", details)
			}
			if f.parking.count() != 0 {
				t.Fatal("a refused wait parked")
			}
		})
	}
}

func TestProgramParkKeepsAShortWaitInline(t *testing.T) {
	program := programtest.NewWaitingProgram(programtest.NewManualClock(), func(now time.Time) commandprogram.WaitState {
		return commandprogram.WaitState{Statement: "sleep 0.2", Until: now.Add(200 * time.Millisecond)}
	})
	f := newParkFixture(t, program)
	done := f.start(t, map[string]any{"command": "sleep 0.2"})
	<-program.Blocked
	program.EndWait()
	result := <-done
	testutil.FailErr(t, "run short wait", result.err)
	if outcome := decodeOutcome(t, result.content); outcome.State != "completed" || outcome.Stdout != "start\ndone\n" || outcome.Status != 0 {
		t.Fatalf("inline outcome %s", result.content)
	}
	if f.parking.count() != 0 || f.clock.Pending() != 0 {
		t.Fatalf("short wait parked %d times with %d timers", f.parking.count(), f.clock.Pending())
	}
	if f.out.SourceRun == nil || f.out.CommandProgram == nil || f.out.CommandProgram.State != "completed" {
		t.Fatalf("inline call effects %+v", f.out)
	}
}

// A parked call resumes with exactly the result the same program returns inline.
func TestProgramParkSettlesWithTheCompleteResult(t *testing.T) {
	inline := programtest.NewWaitingProgram(programtest.NewManualClock(), programtest.SleepState(time.Second))
	reference := newParkFixture(t, inline)
	referenceDone := reference.start(t, map[string]any{"command": "sleep 120; echo done"})
	<-inline.Blocked
	inline.EndWait()
	referenceResult := <-referenceDone
	testutil.FailErr(t, "run inline reference", referenceResult.err)
	want := decodeOutcome(t, referenceResult.content)

	clock := programtest.NewManualClock()
	program := programtest.NewWaitingProgram(clock, programtest.SleepState(120*time.Second))
	f := newParkFixture(t, program)
	runs := &recordedRuns{}
	f.Runtime.SourceRuns = runs
	result := <-f.start(t, map[string]any{"command": "sleep 120; echo done"})
	testutil.FailErr(t, "park waiting program", result.err)
	var parked command.ParkedProgramResult
	testutil.FailErr(t, "decode parked result", json.Unmarshal([]byte(result.content), &parked))
	recorded := <-f.parking.parked
	if parked.State != "parked" || parked.Reason != "sleep 120" || parked.LeaseID != recorded.LeaseID() || parked.WakeAt != clock.Now().Add(120*time.Second).Format(time.RFC3339) {
		t.Fatalf("parked result %s", result.content)
	}
	if recorded.park.Reason != "sleep 120" || recorded.park.ProgramID != parked.ProgramID || recorded.park.Deadline.IsZero() {
		t.Fatalf("park request %+v", recorded.park)
	}
	if programs := f.Programs(t); len(programs) != 1 || programs[0] != "running" {
		t.Fatalf("parked program state %v", programs)
	}
	if f.out.CommandProgram == nil || f.out.CommandProgram.State != "running" || f.out.SourceRun != nil {
		t.Fatalf("parked call effects %+v", f.out)
	}

	program.EndWait()
	got := f.settledProgram(t, recorded)
	if got.State != want.State || got.Status != want.Status || got.Stdout != want.Stdout || got.Stderr != want.Stderr ||
		got.StopCode != want.StopCode || got.ExecutedChildren != want.ExecutedChildren || got.Refusal != nil || got.ProgramID != parked.ProgramID {
		t.Fatalf("parked program settled %+v, inline %+v", got, want)
	}
	if programs := f.Programs(t); programs[0] != "completed" {
		t.Fatalf("settled program state %v", programs)
	}
	if f.out.CommandProgram.State != "running" || f.out.SourceRun != nil {
		t.Fatalf("a parked program's end reached the call's effects: %+v", f.out)
	}
	if recorded := runs.recorded(); len(recorded) != 1 || recorded[0].Command != "sleep 120; echo done" || recorded[0].ExitCode != 0 || recorded[0].Verdict != api.SourceVerdictPassed {
		t.Fatalf("parked run evidence %+v", recorded)
	}
}

// A wait only jobs can end parks if it still holds the program when the
// inline budget runs out on the program clock.
func TestProgramParkParksAJobWaitWhenTheInlineBudgetEnds(t *testing.T) {
	clock := programtest.NewManualClock()
	program := programtest.NewWaitingProgram(clock, func(time.Time) commandprogram.WaitState {
		return commandprogram.WaitState{Statement: "wait %1", Jobs: []string{"bg-1"}}
	})
	f := newParkFixture(t, program)
	done := f.start(t, map[string]any{"command": "long_job & wait %1"})
	<-program.Blocked
	clock.Advance(command.DefaultParkThreshold - time.Second)
	if f.parking.count() != 0 || clock.Pending() != 1 {
		t.Fatalf("job wait parked early: %d parks, %d timers", f.parking.count(), clock.Pending())
	}
	clock.Advance(time.Second)
	result := <-done
	testutil.FailErr(t, "park job wait", result.err)
	var parked command.ParkedProgramResult
	testutil.FailErr(t, "decode parked job wait", json.Unmarshal([]byte(result.content), &parked))
	if parked.Reason != "wait %1" || parked.WakeAt != "" {
		t.Fatalf("parked job wait %s", result.content)
	}
	recorded := <-f.parking.parked
	program.EndWait()
	if outcome := f.settledProgram(t, recorded); outcome.Stdout != "start\ndone\n" {
		t.Fatalf("job wait settled %+v", outcome)
	}
}

// An interruption stops the program at its waiting statement with a typed
// outcome and the output it produced up to the stop.
func TestProgramParkInterruptStopsAtTheWaitingStatement(t *testing.T) {
	program := programtest.NewWaitingProgram(programtest.NewManualClock(), programtest.SleepState(600*time.Second))
	f := newParkFixture(t, program)
	result := <-f.start(t, map[string]any{"command": "sleep 600"})
	testutil.FailErr(t, "park long wait", result.err)
	recorded := <-f.parking.parked
	interrupted := decodeOutcome(t, recorded.park.Interrupt(t.Context()))
	if interrupted.State != "interrupted" || interrupted.Status != 130 || interrupted.StopCode != toolrejection.ToolOwnerInterruptedCode ||
		interrupted.Stdout != "start\n" || interrupted.Refusal == nil || interrupted.Refusal.Code != toolrejection.ToolOwnerInterruptedCode {
		t.Fatalf("interrupted program %+v", interrupted)
	}
	if programs := f.Programs(t); programs[0] != "interrupted" {
		t.Fatalf("interrupted program state %v", programs)
	}
	if f.Runtime.Supervisor.Interrupt(interrupted.ProgramID) {
		t.Fatal("an ended program accepted a second interruption")
	}
}

// A program that does not unwind is reported from its receipts once its
// interruption is abandoned or its grace runs out on the program clock.
func TestProgramParkInterruptReportsAProgramThatDoesNotUnwind(t *testing.T) {
	clock := programtest.NewManualClock()
	program := programtest.NewWaitingProgram(clock, programtest.SleepState(600*time.Second))
	program.IgnoresCancel = true
	f := newParkFixture(t, program)
	result := <-f.start(t, map[string]any{"command": "sleep 600"})
	testutil.FailErr(t, "park long wait", result.err)
	recorded := <-f.parking.parked
	defer func() {
		program.EndWait()
		<-recorded.settled
	}()

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	abandoned := decodeOutcome(t, recorded.park.Interrupt(canceled))
	if abandoned.ProgramID != recorded.park.ProgramID || abandoned.State != "running" || abandoned.StopCode != toolrejection.ToolOwnerInterruptedCode {
		t.Fatalf("abandoned interruption %+v", abandoned)
	}

	reported := make(chan string, 1)
	go func() { reported <- recorded.park.Interrupt(t.Context()) }()
	for clock.Pending() == 0 {
		runtime.Gosched()
	}
	clock.Advance(time.Minute)
	if expired := decodeOutcome(t, <-reported); expired.State != "running" || expired.Status != 130 {
		t.Fatalf("expired interruption %+v", expired)
	}
}

func TestProgramParkStaysInlineWhenTheSessionCannotPark(t *testing.T) {
	program := programtest.NewWaitingProgram(programtest.NewManualClock(), programtest.SleepState(120*time.Second))
	f := newParkFixture(t, program)
	f.parking.unavailable = true
	done := f.start(t, map[string]any{"command": "sleep 120"})
	<-f.parking.refused
	select {
	case result := <-done:
		t.Fatalf("call returned while its program still waited: %+v", result)
	default:
	}
	program.EndWait()
	result := <-done
	testutil.FailErr(t, "run unparked program", result.err)
	if outcome := decodeOutcome(t, result.content); outcome.State != "completed" || outcome.Stdout != "start\ndone\n" {
		t.Fatalf("unparked outcome %s", result.content)
	}
}

// After host loss the program reports from its receipts, never by replay.
func TestProgramReceiptResultReportsALostProgram(t *testing.T) {
	program := programtest.NewWaitingProgram(programtest.NewManualClock(), programtest.SleepState(600*time.Second))
	f := newParkFixture(t, program)
	result := <-f.start(t, map[string]any{"command": "sleep 600"})
	testutil.FailErr(t, "park long wait", result.err)
	recorded := <-f.parking.parked
	interrupted, err := f.Recorder.InterruptRunning(t.Context())
	testutil.FailErr(t, "interrupt running receipts", err)
	if interrupted < 1 {
		t.Fatalf("host loss interrupted %d receipts", interrupted)
	}
	content, err := f.Runtime.ReceiptResult(t.Context(), f.Session, recorded.park.ProgramID)
	testutil.FailErr(t, "report lost program", err)
	lost := decodeOutcome(t, content)
	if lost.ProgramID != recorded.park.ProgramID || lost.State != "interrupted" || lost.Status != 130 || lost.StopCode != toolrejection.ToolOwnerInterruptedCode {
		t.Fatalf("lost program %s", content)
	}
	program.EndWait()
	<-recorded.settled
	if strings.Contains(fmt.Sprint(f.Programs(t)), "completed") {
		t.Fatalf("a lost program settled after reconciliation: %v", f.Programs(t))
	}
}
