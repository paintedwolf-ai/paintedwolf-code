package command_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestProgramContinuationRunsRemainingStatementsOnce(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	calls := 0
	f := programtest.New(t, programtest.Options{Natives: func(string) map[string]command.NativeOperation {
		operation := firstLineOperation()
		prepare := operation.Prepare
		operation.Prepare = func(ctx context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
			prepared, err := prepare(ctx, paths, dispatch, request)
			prepared.Invoke = func(ctx context.Context, _ tools.ToolContext, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
				calls++
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return toolcontract.ProducerResult{Status: 130}, ctx.Err()
				}
				return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
			}
			return prepared, err
		}
		return map[string]command.NativeOperation{"head": operation}
	}})
	clock := programtest.NewManualClock()
	f.Runtime.Clock = clock
	done := make(chan callResult, 1)
	go func() {
		s, e := f.Call(t, t.Context(), "echo listening; head; echo later; exit 7")
		done <- callResult{s, e}
	}()
	<-entered
	clock.Advance(command.DefaultProgramInlineWait)
	got := <-done
	testutil.FailErr(t, "return continued evaluator", got.err)
	var running command.RunningProgramResult
	testutil.FailErr(t, "decode continued evaluator", json.Unmarshal([]byte(got.content), &running))
	probe, err := f.Call(t, t.Context(), "echo probe")
	testutil.FailErr(t, "inspect while original runs", err)
	if result := decodeOutcome(t, probe); result.Stdout != "probe\n" || result.Status != 0 {
		t.Fatalf("probe %+v", result)
	}
	snap, err := f.Background.Snapshot(t.Context(), f.Session, running.Handle, 4096)
	testutil.FailErr(t, "inspect held statement", err)
	if snap.Output != "stdout: listening\n" || snap.HasExit {
		t.Fatalf("early output %+v", snap)
	}
	close(release)
	status, _, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
	testutil.FailErr(t, "join original evaluator", err)
	snap, err = f.Background.Snapshot(t.Context(), f.Session, running.Handle, 4096)
	testutil.FailErr(t, "inspect final output", err)
	if status != 7 || calls != 1 || strings.Count(snap.Output, "stdout: later\n") != 1 {
		t.Fatalf("status=%d calls=%d output=%s", status, calls, snap.Output)
	}
}
