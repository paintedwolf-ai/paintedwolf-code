//go:build unix

package command_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// M07 (scoped cancellation witness): Demonstrates continued-program cancellation
// and terminal delivery after Lifecycle.Stop on a substitute evaluator.
// This witness does not establish the later-statement or admission fence.
func TestBB05_M07_ContinuedProgramStopCancellationWitness(t *testing.T) {
	f := programtest.New(t, programtest.Options{})
	clock := programtest.NewManualClock()
	f.Runtime.Clock = clock

	entered := make(chan struct{})
	f.Runtime.Evaluate = func(ctx context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		_, _ = fmt.Fprint(ports.Streams.Output, "before-stop\n")
		close(entered)
		<-ctx.Done()
		return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
	}

	done := make(chan callResult, 1)
	go func() {
		out, err := f.Call(t, t.Context(), "continued-work")
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
	if !strings.Contains(stdout, "before-stop\n") {
		t.Fatalf("continued program lost earlier output: stdout=%q", stdout)
	}
	snap, err := f.Background.Output.ReadCommandOutput(t.Context(), f.Session, running.Handle, 0)
	testutil.FailErr(t, "read continued program output", err)
	if snap.ProgramStopCode != toolrejection.ToolOwnerInterruptedCode || snap.TerminationReason != bgprocess.TerminationStoppedByAgent {
		t.Fatalf("continued program terminal state = %+v", snap)
	}
}

// M07 (real dispatch + positive counterpart + ST01-ST03 admission fence pin):
// Drives real commandprogram.Evaluate over a gated native stage (`head; echo later-marker`).
// The positive subtest proves `echo later-marker` executes when admitted after continuation.
// The stopped subtest exercises Lifecycle.Stop after public continuation acknowledgment and
// pins the ST01-ST03 prerequisite for closing program admission in StopProgram.
func TestBB05_M07_ContinuedProgramStopDispatchAndPositiveCounterpart(t *testing.T) {
	for _, stop := range []bool{false, true} {
		name := "positive_admitted_runs_later_marker"
		if stop {
			name = "stopped_after_continuation_acknowledgment"
		}
		t.Run(name, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})

			f := programtest.New(t, programtest.Options{Natives: func(string) map[string]command.NativeOperation {
				op := firstLineOperation()
				prepare := op.Prepare
				op.Prepare = func(ctx context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
					prepared, err := prepare(ctx, paths, dispatch, request)
					if err != nil {
						return command.NativePrepared{}, err
					}
					prepared.Invoke = func(ctx context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
						_, _ = fmt.Fprint(streams.Output, "before-gate\n")
						close(entered)
						select {
						case <-release:
							return toolcontract.ProducerResult{
								Status:              0,
								Terminal:            toolcontract.ProducerCompleted,
								Complete:            true,
								PublicationEligible: true,
								EffectCertainty:     toolcontract.NoEffect,
							}, nil
						case <-ctx.Done():
							return toolcontract.ProducerResult{
								Status:          130,
								Terminal:        toolcontract.ProducerInterrupted,
								StopCode:        toolrejection.ToolOwnerInterruptedCode,
								EffectCertainty: toolcontract.NoEffect,
							}, ctx.Err()
						}
					}
					return prepared, nil
				}
				return map[string]command.NativeOperation{"head": op}
			}})

			clock := programtest.NewManualClock()
			f.Runtime.Clock = clock

			done := make(chan callResult, 1)
			go func() {
				out, err := f.Call(t, t.Context(), "head; echo later-marker")
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

			if stop {
				_, err := f.Background.Lifecycle.Stop(f.Session, running.Handle, bgprocess.TerminationStoppedByAgent)
				testutil.FailErr(t, "stop continued program", err)
			} else {
				close(release)
			}

			status, wasStopped, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
			testutil.FailErr(t, "join continued program", err)
			stdout, _ := continuationStreams(t, f, running.Handle)

			if !stop {
				if status != 0 || wasStopped || stdout != "before-gate\nlater-marker\n" {
					t.Fatalf("positive continuation: status=%d stopped=%v stdout=%q", status, wasStopped, stdout)
				}
				return
			}

			if !wasStopped {
				t.Fatal("stopped continuation Wait reported stopped=false")
			}
			if !strings.Contains(stdout, "before-gate\n") || strings.Contains(stdout, "later-marker") {
				t.Fatalf("stopped continuation output mismatch: stdout=%q", stdout)
			}
			snap, err := f.Background.Output.ReadCommandOutput(t.Context(), f.Session, running.Handle, 0)
			testutil.FailErr(t, "read stopped continuation output", err)
			if snap.ProgramStopCode != toolrejection.ToolOwnerInterruptedCode || snap.TerminationReason != bgprocess.TerminationStoppedByAgent {
				t.Fatalf("stopped continuation terminal state = %+v", snap)
			}
		})
	}
}
