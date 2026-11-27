//go:build unix

package command_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// I15 (Handles and recovery preserve scope and cardinality):
// Verifies handle isolation across two sessions sharing one bgprocess.Registry,
// refusal of unknown/cross-session handles, and preservation of unrelated jobs.
// Explicit Stop admission fence remains gated by ST01-ST03.
func TestI15_HandleScopeIsolationAndUnrelatedJobUsability(t *testing.T) {
	ctx := t.Context()
	sharedReg := bgprocess.NewRegistry(bgprocess.Config{MaxBackground: 8}, bgprocess.Hooks{})
	sharedReg.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
	t.Cleanup(func() { _ = sharedReg.Lifecycle.Close(t.Context()) })

	clockA := programtest.NewManualClock()
	clockB := programtest.NewManualClock()
	fA := programtest.New(t, programtest.Options{Session: "i15-sess-a", Background: sharedReg})
	fB := programtest.New(t, programtest.Options{Session: "i15-sess-b", Background: sharedReg})
	fA.Runtime.Clock = clockA
	fB.Runtime.Clock = clockB

	// Start two independent jobs in session A and one job in session B.
	outA1, err := fA.Call(t, ctx, "sleep 60 &")
	testutil.FailErr(t, "start job A1", err)
	_ = decodeOutcome(t, outA1)
	outA2, err := fA.Call(t, ctx, "sleep 60 &")
	testutil.FailErr(t, "start job A2", err)
	_ = decodeOutcome(t, outA2)
	outB1, err := fB.Call(t, ctx, "sleep 60 &")
	testutil.FailErr(t, "start job B1", err)
	_ = decodeOutcome(t, outB1)

	jobsA := fA.Runtime.Supervisor.Jobs(fA.Session, "default")
	jobsB := fB.Runtime.Supervisor.Jobs(fB.Session, "default")
	hA1, err := jobsA.Resolve("%1")
	testutil.FailErr(t, "resolve A %1", err)
	hA2, err := jobsA.Resolve("%2")
	testutil.FailErr(t, "resolve A %2", err)
	hB1, err := jobsB.Resolve("%1")
	testutil.FailErr(t, "resolve B %1", err)
	if hA1 == hA2 || hA1 == hB1 || hA2 == hB1 {
		t.Fatalf("handle collision across jobs/sessions: A1=%q A2=%q B1=%q", hA1, hA2, hB1)
	}

	outTool := &command.CommandOutputTool{Registry: sharedReg}
	stopTool := &command.CommandStopTool{Registry: sharedReg}
	tcA := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: fA.Session}}
	tcB := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: fB.Session}}

	// Own session reads hA1 cleanly; running state is distinct from exit (ExitCode == nil).
	ownRaw, err := outTool.Run(ctx, map[string]any{"handle": hA1, "cursor": float64(0)}, tcA)
	testutil.FailErr(t, "read own handle A1", err)
	var ownOut struct {
		Handle   string `json:"handle"`
		Running  bool   `json:"running"`
		ExitCode *int   `json:"exit_code"`
	}
	testutil.FailErr(t, "decode own handle output", json.Unmarshal([]byte(ownRaw), &ownOut))
	if ownOut.Handle != hA1 || !ownOut.Running || ownOut.ExitCode != nil {
		t.Fatalf("own live handle state = %+v, want running with nil exit_code", ownOut)
	}

	// Second session cannot read or stop hA1 (typed cross-session refusal).
	if _, err := outTool.Run(ctx, map[string]any{"handle": hA1, "cursor": float64(0)}, tcB); err == nil {
		t.Fatal("session B read session A handle")
	} else if rej := toolrejection.AsToolReject(err); rej == nil || rej.Code != "BACKGROUND_HANDLE_NOT_FOUND" {
		t.Fatalf("cross-session output refusal = %v, want BACKGROUND_HANDLE_NOT_FOUND", err)
	}
	if _, err := stopTool.Run(ctx, map[string]any{"handle": hA1}, tcB); err == nil {
		t.Fatal("session B stopped session A handle")
	} else if rej := toolrejection.AsToolReject(err); rej == nil || rej.Code != "BACKGROUND_HANDLE_NOT_FOUND" {
		t.Fatalf("cross-session stop refusal = %v, want BACKGROUND_HANDLE_NOT_FOUND", err)
	}

	// Stop hA1 in its own session; verify unrelated hA2 and session B's hB1 remain usable.
	stopRaw, err := stopTool.Run(ctx, map[string]any{"handle": hA1}, tcA)
	testutil.FailErr(t, "stop own handle A1", err)
	if !strings.Contains(stopRaw, hA1) {
		t.Fatalf("stop output missing handle %q: %s", hA1, stopRaw)
	}
	_, stopped, err := sharedReg.Lifecycle.Wait(ctx, fA.Session, hA1)
	testutil.FailErr(t, "wait stopped A1", err)
	if !stopped {
		t.Fatal("A1 Wait reported stopped=false")
	}

	// Unrelated job A2 and peer session job B1 stay running and usable.
	for _, tc := range []struct {
		name   string
		handle string
		tctx   tools.ToolContext
	}{{"unrelated A2", hA2, tcA}, {"peer B1", hB1, tcB}} {
		raw, rErr := outTool.Run(ctx, map[string]any{"handle": tc.handle, "cursor": float64(0)}, tc.tctx)
		testutil.FailErr(t, "read "+tc.name, rErr)
		var st struct {
			Handle   string `json:"handle"`
			Running  bool   `json:"running"`
			ExitCode *int   `json:"exit_code"`
		}
		testutil.FailErr(t, "decode "+tc.name, json.Unmarshal([]byte(raw), &st))
		if st.Handle != tc.handle || !st.Running || st.ExitCode != nil {
			t.Fatalf("%s disturbed by stopping A1: %+v", tc.name, st)
		}
	}

	// Cleanly stop remaining jobs and verify empty-session missing-handle code.
	_, err = stopTool.Run(ctx, map[string]any{"handle": hA2}, tcA)
	testutil.FailErr(t, "stop A2", err)
	_, _, err = sharedReg.Lifecycle.Wait(ctx, fA.Session, hA2)
	testutil.FailErr(t, "wait A2", err)
	_, err = stopTool.Run(ctx, map[string]any{"handle": hB1}, tcB)
	testutil.FailErr(t, "stop B1", err)
	_, _, err = sharedReg.Lifecycle.Wait(ctx, fB.Session, hB1)
	testutil.FailErr(t, "wait B1", err)

	if _, err := outTool.Run(ctx, map[string]any{"handle": "nonexistent"}, tcA); err == nil {
		t.Fatal("nonexistent handle succeeded")
	} else if rej := toolrejection.AsToolReject(err); rej == nil || rej.Code != "COMMAND_OUTPUT_NO_LIVE_JOB" {
		t.Fatalf("empty-session missing handle refusal = %v, want COMMAND_OUTPUT_NO_LIVE_JOB", err)
	}
}

// I15: Cancel during parked wait keeps pending/interrupted state distinct from
// job exit, preserves the unrelated/waited background job, and allows reuse.
// Explicit Stop admission fence remains gated by ST01-ST03.
func TestI15_CancelDuringWaitKeepsPendingInterruptedDistinctFromExit(t *testing.T) {
	ctx := t.Context()
	f := newProgramFixture(t)
	clock := programtest.NewManualClock()
	f.Runtime.Clock = clock
	parking := newRecordingParking()
	f.Runtime.Parking = parking

	outBG, err := f.call(t, ctx, "sleep 60 &")
	testutil.FailErr(t, "start background job", err)
	_ = decodeOutcome(t, outBG)

	jobs := f.Runtime.Supervisor.Jobs(f.Session, "default")
	handle, err := jobs.Resolve("%1")
	testutil.FailErr(t, "resolve %1", err)

	waitDone := make(chan callResult, 1)
	go func() {
		res, wErr := f.call(t, ctx, "wait %1")
		waitDone <- callResult{res, wErr}
	}()
	testutil.WaitFor(t, "wait %1 park timer scheduled", func() bool {
		return clock.Pending() >= 2
	})
	clock.Advance(toolcontract.ProgramParkThreshold)
	parkedRes := <-waitDone
	testutil.FailErr(t, "park wait %1", parkedRes.err)
	var parked command.ParkedProgramResult
	testutil.FailErr(t, "decode parked wait", json.Unmarshal([]byte(parkedRes.content), &parked))
	if parked.State != "parked" || parked.ProgramID == "" {
		t.Fatalf("expected parked wait, got %+v", parked)
	}

	// While parked, durable receipt reports running (distinct from completed exit).
	projBefore, err := f.Recorder.ProjectProgram(ctx, f.Session, parked.ProgramID, 1)
	testutil.FailErr(t, "project parked wait", err)
	if projBefore.Program.State != "running" || projBefore.Program.StatusCode != nil {
		t.Fatalf("parked wait receipt collapsed into exit: %+v", projBefore.Program)
	}

	// Interrupt the parked wait; wait settles interrupted while job %1 remains running.
	recPark := <-parking.parked
	interrupted := decodeOutcome(t, recPark.park.Interrupt(ctx))
	if interrupted.ProgramID != parked.ProgramID || interrupted.State != "interrupted" ||
		interrupted.Status != 130 || interrupted.StopCode != toolrejection.ToolOwnerInterruptedCode {
		t.Fatalf("interrupted wait outcome = %+v", interrupted)
	}
	<-recPark.settled

	snap, err := f.Background.Output.ReadCommandOutput(ctx, f.Session, handle, 0)
	testutil.FailErr(t, "read background job after wait interrupt", err)
	if !snap.Output.Running || snap.Output.ExitCode != nil {
		t.Fatalf("interrupting parked wait exited background job: %+v", snap.Output)
	}

	// Stop the job and confirm session reuse succeeds cleanly.
	_, err = f.Background.Lifecycle.Stop(f.Session, handle, bgprocess.TerminationStoppedByAgent)
	testutil.FailErr(t, "stop background job", err)
	_, _, err = f.Background.Lifecycle.Wait(ctx, f.Session, handle)
	testutil.FailErr(t, "wait stopped background job", err)

	reusedRaw, err := f.call(t, ctx, "echo i15-reuse-ok")
	testutil.FailErr(t, "reuse session after wait cancel", err)
	reused := decodeOutcome(t, reusedRaw)
	if reused.State != "completed" || reused.Status != 0 || reused.Stdout != "i15-reuse-ok\n" {
		t.Fatalf("session reuse outcome = %+v", reused)
	}
}
