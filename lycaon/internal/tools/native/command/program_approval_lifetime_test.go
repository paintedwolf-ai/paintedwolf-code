package command_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestProgramApprovalSurvivesTurnAndHonorsStop(t *testing.T) {
	for _, background := range []bool{false, true} {
		for _, stop := range []bool{false, true} {
			t.Run(fmt.Sprintf("background=%t/stop=%t", background, stop), func(t *testing.T) {
				approval := &programApprovalPoller{entered: make(chan context.Context, 1), answer: make(chan struct{})}
				f := programtest.New(t, programtest.Options{Natives: func(string) map[string]command.NativeOperation {
					operation := firstLineOperation()
					prepare := operation.Prepare
					operation.Prepare = func(ctx context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
						prepared, err := prepare(ctx, paths, dispatch, request)
						prepared.Invoke = func(ctx context.Context, _ tools.ToolContext, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
							_, err := hitl.WaitForCheckpoint(ctx, approval, "approval")
							return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, err
						}
						return prepared, err
					}
					return map[string]command.NativeOperation{"head": operation}
				}})
				clock := programtest.NewManualClock()
				f.Runtime.Clock = clock
				turn, endTurn := context.WithCancel(t.Context())
				defer endTurn()
				caller := hitl.WithStopContext(turn, turn)
				source := "head; echo continued"
				if background {
					source = "head & wait; echo continued"
				}
				done := make(chan callResult, 1)
				go func() {
					content, err := f.Call(t, caller, source)
					done <- callResult{content, err}
				}()
				waitCtx := <-approval.entered
				clock.Advance(command.DefaultProgramInlineWait)
				got := <-done
				testutil.FailErr(t, "promote approval wait", got.err)
				var running command.RunningProgramResult
				testutil.FailErr(t, "decode continuation", json.Unmarshal([]byte(got.content), &running))
				if running.Handle == "" {
					t.Fatalf("missing continuation: %s", got.content)
				}
				endTurn()
				if err := waitCtx.Err(); err != nil {
					t.Fatalf("turn completion canceled approval: %v", err)
				}
				if stop {
					handle := running.Handle
					if background {
						for _, job := range f.Background.ActiveJobs(f.Session) {
							if job.Handle != running.Handle {
								handle = job.Handle
							}
						}
						if handle == running.Handle {
							t.Fatal("missing statement job")
						}
					}
					_, err := f.Background.Lifecycle.Stop(f.Session, handle, bgprocess.TerminationStoppedByAgent)
					testutil.FailErr(t, "stop approval owner", err)
				} else {
					close(approval.answer)
				}
				status, _, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, running.Handle)
				testutil.FailErr(t, "join approval continuation", err)
				stdout, _ := continuationStreams(t, f, running.Handle)
				if !stop && (status != 0 || stdout != "continued\n") {
					t.Fatalf("approved continuation: status=%d output=%q", status, stdout)
				}
				if stop && !background && strings.Contains(stdout, "continued") {
					t.Fatal("stopped foreground executed the next statement")
				}
				if stop && waitCtx.Err() == nil {
					t.Fatal("owner stop did not cancel approval")
				}
			})
		}
	}
}

type programApprovalPoller struct {
	entered chan context.Context
	answer  chan struct{}
}

func (p *programApprovalPoller) ObserveCheckpointWait(ctx context.Context, _ string) func() {
	p.entered <- hitl.WaitContext(ctx)
	return func() {}
}

func (p *programApprovalPoller) PollCheckpoint(ctx context.Context, _ string) (*hitl.CheckpointResponse, error) {
	select {
	case <-p.answer:
		return &hitl.CheckpointResponse{Status: hitl.DecisionStatusApproved}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
