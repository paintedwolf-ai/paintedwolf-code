package bgprocess_test

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProgramJobCompetingStopsWaitForClosureNotCleanup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  bgprocess.TerminationReason
		second bgprocess.TerminationReason
	}{
		{name: "user_then_agent", first: bgprocess.TerminationStoppedByUser, second: bgprocess.TerminationStoppedByAgent},
		{name: "agent_then_user", first: bgprocess.TerminationStoppedByAgent, second: bgprocess.TerminationStoppedByUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				completed := make(chan bgprocess.Completion, 4)
				reg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
					Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
				})
				defer func() { _ = reg.Lifecycle.Close(context.Background()) }()
				enteredOnStop := make(chan struct{})
				releaseOnStop := make(chan struct{})
				releaseCleanup := make(chan struct{})
				var onStopCalls atomic.Int32
				var closed atomic.Bool
				var gotReason atomic.Value

				handle, err := reg.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
					SessionID: "sess-1", ProjectID: "proj-1", CommandLine: "long-program",
					OnStop: func(reason bgprocess.TerminationReason) {
						onStopCalls.Add(1)
						gotReason.Store(reason)
						close(enteredOnStop)
						<-releaseOnStop
						closed.Store(true)
					},
					Run: func(ctx context.Context, _, _ io.Writer) (bgprocess.ProgramJobResult, error) {
						<-ctx.Done()
						<-releaseCleanup
						return bgprocess.ProgramJobResult{ExitCode: 130}, ctx.Err()
					},
				})
				testutil.FailErr(t, "start program job", err)

				type stopResp struct {
					res    *api.BackgroundProcessStopResult
					closed bool
					err    error
				}
				firstDone := make(chan stopResp, 1)
				go func() {
					res, err := reg.Lifecycle.Stop("sess-1", handle, tc.first)
					firstDone <- stopResp{res: res, closed: closed.Load(), err: err}
				}()
				<-enteredOnStop

				secondDone := make(chan stopResp, 1)
				go func() {
					res, err := reg.Lifecycle.Stop("sess-1", handle, tc.second)
					secondDone <- stopResp{res: res, closed: closed.Load(), err: err}
				}()

				// Wait deterministically until the second caller is blocked on closure.
				synctest.Wait()
				select {
				case resp := <-secondDone:
					t.Fatalf("second stop returned before OnStop finished: %+v", resp)
				default:
				}

				// Release OnStop while child cleanup is still blocked.
				close(releaseOnStop)

				r1 := <-firstDone
				r2 := <-secondDone
				testutil.FailErr(t, "first stop", r1.err)
				testutil.FailErr(t, "second stop", r2.err)
				if !r1.closed || !r2.closed {
					t.Fatalf("acknowledgment returned before program closure: r1.closed=%t r2.closed=%t", r1.closed, r2.closed)
				}
				if !r1.res.Running || !r2.res.Running {
					t.Fatalf("expected running=true while cleanup held: r1=%+v r2=%+v", r1.res, r2.res)
				}
				wantAPI := api.BackgroundProcessTerminationReason(tc.first)
				if r1.res.TerminationReason != wantAPI || r2.res.TerminationReason != wantAPI {
					t.Fatalf("reasons r1=%q r2=%q, want %q", r1.res.TerminationReason, r2.res.TerminationReason, wantAPI)
				}
				if onStopCalls.Load() != 1 || gotReason.Load() != tc.first {
					t.Fatalf("OnStop calls=%d reason=%v, want 1 and %q", onStopCalls.Load(), gotReason.Load(), tc.first)
				}

				close(releaseCleanup)
				c := <-completed
				if c.TerminationReason != tc.first || c.ExitCode != 130 {
					t.Fatalf("completion reason=%q exit=%d, want %q and 130", c.TerminationReason, c.ExitCode, tc.first)
				}
				select {
				case dup := <-completed:
					t.Fatalf("unexpected duplicate completion: %+v", dup)
				default:
				}
			})
		})
	}
}

func TestProgramJobEarliestVisibleStopDuringProjectPublish(t *testing.T) {
	completed := make(chan bgprocess.Completion, 2)
	var closed atomic.Bool
	var onStopCalls atomic.Int32
	var stopErr atomic.Value
	var stopRes atomic.Value
	var reg *bgprocess.Registry

	reg = newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
		PublishProject: func(_ context.Context, ev api.ProjectBackgroundEvent) {
			if ev.Action != "started" {
				return
			}
			res, err := reg.Lifecycle.StopProjectProcess(ev.ProjectID, ev.ProcessID, bgprocess.TerminationStoppedByUser)
			if err != nil {
				stopErr.Store(err)
			}
			if res != nil {
				stopRes.Store(res)
			}
			if !closed.Load() {
				t.Errorf("StopProjectProcess returned before OnStop closed admission")
			}
		},
	})

	handle, err := reg.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
		SessionID: "sess-1", ProjectID: "proj-1", CommandLine: "early-stop",
		OnStop: func(reason bgprocess.TerminationReason) {
			onStopCalls.Add(1)
			if reason != bgprocess.TerminationStoppedByUser {
				t.Errorf("OnStop reason = %q, want stopped_by_user", reason)
			}
			closed.Store(true)
		},
		Run: func(ctx context.Context, _, _ io.Writer) (bgprocess.ProgramJobResult, error) {
			<-ctx.Done()
			return bgprocess.ProgramJobResult{ExitCode: 130}, ctx.Err()
		},
	})
	testutil.FailErr(t, "start program job", err)
	if errVal := stopErr.Load(); errVal != nil {
		t.Fatalf("earliest stop failed: %v", errVal)
	}
	res, _ := stopRes.Load().(*api.BackgroundProcessStopResult)
	if res == nil || !res.StopRequested || res.TerminationReason != api.BackgroundProcessTerminationReason(bgprocess.TerminationStoppedByUser) {
		t.Fatalf("earliest stop result = %+v", res)
	}
	if onStopCalls.Load() != 1 || !closed.Load() {
		t.Fatalf("OnStop calls=%d closed=%t", onStopCalls.Load(), closed.Load())
	}
	exitCode, stopped, err := reg.Lifecycle.Wait(t.Context(), "sess-1", handle)
	testutil.FailErr(t, "wait earliest stopped job", err)
	if !stopped || exitCode != 130 {
		t.Fatalf("wait exit=%d stopped=%t", exitCode, stopped)
	}
	c := testutil.Receive(t, "completion", completed)
	if c.TerminationReason != bgprocess.TerminationStoppedByUser {
		t.Fatalf("completion reason = %q, want stopped_by_user", c.TerminationReason)
	}
}

func TestProgramJobNormalExitSkipsOnStopAndRetainsExitedOnLateStop(t *testing.T) {
	completed := make(chan bgprocess.Completion, 2)
	reg := newTestRegistry(t, bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c },
	})
	var onStopCalls atomic.Int32
	handle, err := reg.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
		SessionID: "sess-1", ProjectID: "proj-1", CommandLine: "normal-exit",
		OnStop: func(bgprocess.TerminationReason) { onStopCalls.Add(1) },
		Run: func(_ context.Context, stdout, _ io.Writer) (bgprocess.ProgramJobResult, error) {
			_, _ = io.WriteString(stdout, "done\n")
			return bgprocess.ProgramJobResult{ExitCode: 7}, nil
		},
	})
	testutil.FailErr(t, "start normal job", err)
	exitCode, stopped, err := reg.Lifecycle.Wait(t.Context(), "sess-1", handle)
	testutil.FailErr(t, "wait normal job", err)
	if stopped || exitCode != 7 {
		t.Fatalf("wait exit=%d stopped=%t, want 7/false", exitCode, stopped)
	}
	if onStopCalls.Load() != 0 {
		t.Fatalf("normal exit invoked OnStop %d times", onStopCalls.Load())
	}
	c := testutil.Receive(t, "completion", completed)
	if c.TerminationReason != bgprocess.TerminationExited || c.ExitCode != 7 {
		t.Fatalf("completion reason=%q exit=%d", c.TerminationReason, c.ExitCode)
	}

	// Late Stop on an already-exited handle preserves exited state and skips OnStop.
	res, err := reg.Lifecycle.Stop("sess-1", handle, bgprocess.TerminationStoppedByUser)
	testutil.FailErr(t, "late stop", err)
	if res.Running || res.ExitCode == nil || *res.ExitCode != 7 || res.TerminationReason != api.BackgroundProcessTerminationReason(bgprocess.TerminationExited) {
		t.Fatalf("late stop result = %+v", res)
	}
	if onStopCalls.Load() != 0 {
		t.Fatalf("late stop invoked OnStop %d times", onStopCalls.Load())
	}
}

func TestTerminationReasonFromContextReadsCancelCauseAndRespectsOuterValue(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(bgprocess.WithCauseReason(context.Canceled, bgprocess.TerminationStoppedByUser))
	if got := bgprocess.TerminationReasonFromContext(ctx, bgprocess.TerminationStoppedByAgent); got != bgprocess.TerminationStoppedByUser {
		t.Fatalf("cause reason = %q, want stopped_by_user", got)
	}

	outer := bgprocess.WithTerminationReason(t.Context(), bgprocess.TerminationChatDeleted)
	ctx2, cancel2 := context.WithCancelCause(outer)
	cancel2(bgprocess.WithCauseReason(context.Canceled, bgprocess.TerminationStoppedByUser))
	if got := bgprocess.TerminationReasonFromContext(ctx2, bgprocess.TerminationStoppedByAgent); got != bgprocess.TerminationChatDeleted {
		t.Fatalf("outer reason = %q, want chat_deleted", got)
	}
}
