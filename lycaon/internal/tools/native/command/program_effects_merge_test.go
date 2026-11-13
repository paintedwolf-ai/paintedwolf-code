package command_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// effectStage is what the `head` stage does on its nth invocation.
type effectStage func(call int, tc tools.ToolContext) error

// effectFixture binds `head` to a stage that records effects on its own
// outputs, and collects the call's outputs.
func effectFixture(t *testing.T, stage effectStage) (*programtest.Fixture, *tools.ToolInvocationOut) {
	t.Helper()
	var calls atomic.Int32
	f := programtest.New(t, programtest.Options{Natives: func(string) map[string]command.NativeOperation {
		operation := firstLineOperation()
		prepare := operation.Prepare
		operation.Prepare = func(ctx context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
			prepared, err := prepare(ctx, paths, dispatch, request)
			prepared.Invoke = func(_ context.Context, tc tools.ToolContext, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
				if err := stage(int(calls.Add(1)), tc); err != nil {
					return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
				}
				return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
			}
			return prepared, err
		}
		return map[string]command.NativeOperation{"head": operation}
	}})
	out := &tools.ToolInvocationOut{}
	f.Configure = func(tc *tools.ToolContext) { tc.Effects.Out = out }
	return f, out
}

func recordEdit(tc tools.ToolContext, path string) {
	tc.Effects.Out.RecordFileEdit(api.FileEditSnapshot{Path: path, After: "after"})
}

func recordEveryEffect(tc tools.ToolContext) {
	recordEdit(tc, "a.go")
	tc.Effects.Out.Listener = &api.LoopbackListenerFact{}
	tc.Effects.Out.SourceReads = append(tc.Effects.Out.SourceReads, agentpresence.Read{Target: agentpresence.Target{RootID: "root", Path: "a.go"}})
	tc.Effects.Out.SourceContext = &api.SourceContext{Locations: []api.NavigationTarget{
		{ProjectID: "project", RootID: "root", Path: "a.go", EntryKind: api.NavigationEntryKindFile},
	}}
}

// editedPath is the one file the call's outputs hold an edit for.
func editedPath(out *tools.ToolInvocationOut) string {
	if len(out.FileEdits) != 1 {
		return ""
	}
	return out.FileEdits[0].Path
}

func TestProgramCarriesStageEffectsToItsCall(t *testing.T) {
	f, out := effectFixture(t, func(_ int, tc tools.ToolContext) error {
		recordEveryEffect(tc)
		return nil
	})
	raw, err := f.Call(t, t.Context(), "head")
	testutil.FailErr(t, "run program", err)
	if outcome := decodeOutcome(t, raw); outcome.Status != 0 {
		t.Fatalf("outcome %+v", outcome)
	}
	if editedPath(out) != "a.go" || out.Listener == nil || len(out.SourceReads) != 1 ||
		out.SourceContext == nil || len(out.SourceContext.Locations) != 1 || out.CommandProgram == nil {
		t.Fatalf("call outputs lost a stage effect: edit=%q listener=%v reads=%d context=%+v receipt=%v",
			editedPath(out), out.Listener != nil, len(out.SourceReads), out.SourceContext, out.CommandProgram != nil)
	}
}

// An effect that already happened is recorded whether its stage, or a later
// statement, fails.
func TestProgramCarriesEffectsOfFailedWork(t *testing.T) {
	for name, tc := range map[string]struct {
		source string
		fail   bool
	}{
		"stage fails after writing": {source: "head", fail: true},
		"later statement fails":     {source: "head; exit 3"},
	} {
		t.Run(name, func(t *testing.T) {
			f, out := effectFixture(t, func(_ int, stage tools.ToolContext) error {
				recordEveryEffect(stage)
				if tc.fail {
					return errors.New("stage failed after writing")
				}
				return nil
			})
			_, _ = f.Call(t, t.Context(), tc.source)
			if editedPath(out) != "a.go" || out.Listener == nil || len(out.SourceReads) != 1 || out.SourceContext == nil {
				t.Fatalf("failed work dropped a recorded effect: edit=%q listener=%v reads=%d context=%+v",
					editedPath(out), out.Listener != nil, len(out.SourceReads), out.SourceContext)
			}
		})
	}
}

// A call that returns while its program runs carries what the program did so
// far; later effects stay off that call's outputs.
func TestBackgroundedProgramCarriesEffectsRecordedBeforeItsCallReturned(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f, out := effectFixture(t, func(call int, tc tools.ToolContext) error {
		if call == 1 {
			recordEveryEffect(tc)
			return nil
		}
		close(entered)
		<-release
		recordEdit(tc, "late.go")
		return nil
	})
	clock := programtest.NewManualClock()
	f.Runtime.Clock = clock
	done := make(chan callResult, 1)
	go func() {
		s, e := f.Call(t, t.Context(), "head; head")
		done <- callResult{s, e}
	}()
	<-entered
	clock.Advance(command.ProgramInlineWait())
	got := <-done
	testutil.FailErr(t, "return running program", got.err)
	var running command.RunningProgramResult
	testutil.FailErr(t, "decode running program", json.Unmarshal([]byte(got.content), &running))
	if running.State != "running" || editedPath(out) != "a.go" || out.Listener == nil || len(out.SourceReads) != 1 || out.SourceContext == nil {
		t.Fatalf("running result %+v lost an earlier effect: edit=%q listener=%v reads=%d context=%+v",
			running, editedPath(out), out.Listener != nil, len(out.SourceReads), out.SourceContext)
	}
	close(release)
	_, _, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
	testutil.FailErr(t, "join program", err)
	if editedPath(out) != "a.go" || len(out.SourceReads) != 1 {
		t.Fatalf("an effect recorded after the call returned reached its outputs: edit=%q reads=%d", editedPath(out), len(out.SourceReads))
	}
}

func TestParkedProgramCarriesEffectsRecordedBeforeItParked(t *testing.T) {
	waiting := programtest.NewWaitingProgram(programtest.NewManualClock(), programtest.SleepState(120*time.Second))
	f, out := effectFixture(t, func(_ int, tc tools.ToolContext) error {
		recordEveryEffect(tc)
		return nil
	})
	parking := newRecordingParking()
	f.Runtime.Clock, f.Runtime.Parking = waiting.Clock, parking
	// The program runs its statements, then waits as `sleep` would.
	f.Runtime.Evaluate = func(ctx context.Context, snapshot commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		if outcome, err := commandprogram.Evaluate(ctx, snapshot, ports); err != nil {
			return outcome, err
		}
		return waiting.Evaluate(ctx, snapshot, ports)
	}
	raw, err := f.Call(t, t.Context(), "head")
	testutil.FailErr(t, "park program", err)
	var parked command.ParkedProgramResult
	testutil.FailErr(t, "decode parked program", json.Unmarshal([]byte(raw), &parked))
	recorded := <-parking.parked
	if parked.State != "parked" || editedPath(out) != "a.go" || out.Listener == nil || len(out.SourceReads) != 1 || out.SourceContext == nil {
		t.Fatalf("parked result %+v lost an earlier effect: edit=%q listener=%v reads=%d context=%+v",
			parked, editedPath(out), out.Listener != nil, len(out.SourceReads), out.SourceContext)
	}
	waiting.EndWait()
	settled := <-recorded.settled
	testutil.FailErr(t, "settle parked program", settled.err)
}
