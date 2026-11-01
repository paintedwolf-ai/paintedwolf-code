//go:build unix

package exec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPipelineTimeoutReportsEffectiveDeadline(t *testing.T) {
	for _, mode := range []string{"command", "caller", "async"} {
		t.Run(mode, func(t *testing.T) {
			cause := errors.New("caller budget exhausted")
			ctx := t.Context()
			opts := ExecOpts{Launch: HostLaunch("timeout test"), Timeout: time.Hour}
			var deadline time.Time
			if mode == "command" {
				opts.Timeout = 150 * time.Millisecond
			} else {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeoutCause(ctx, 150*time.Millisecond, cause)
				defer cancel()
				deadline, _ = ctx.Deadline()
			}
			stages := []Stage{{Name: "sleep", Args: []string{"10"}}}
			var result *PipelineResult
			var err error
			if mode == "async" {
				var run *AsyncPipeline
				run, err = StartPipelineAsync(ctx, stages, opts, nil, nil)
				testutil.FailErr(t, "start async timeout", err)
				result, err = run.Wait()
			} else {
				result, err = RunPipeline(ctx, stages, opts)
			}
			var timeout *TimeoutError
			if !errors.As(err, &timeout) || !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout identity lost: %v", err)
			}
			if result == nil || !result.TimedOut || timeout.Elapsed <= 0 {
				t.Fatalf("timeout measurements: result=%+v error=%+v", result, timeout)
			}
			if timeout.CommandDeadline != (mode == "command") || timeout.Deadline.IsZero() {
				t.Fatalf("deadline attribution = %+v", timeout)
			}
			if mode != "command" && (!timeout.Deadline.Equal(deadline) || !errors.Is(err, cause)) {
				t.Fatalf("caller deadline or cause lost: %+v", timeout)
			}
		})
	}
}

func TestPipelineCancellationIsNotTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	run, err := StartPipelineAsync(ctx, []Stage{{Name: "sleep", Args: []string{"10"}}}, ExecOpts{Launch: HostLaunch("cancel test")}, nil, nil)
	testutil.FailErr(t, "start cancel test", err)
	cancel()
	result, err := run.Wait()
	if result == nil || result.TimedOut || errors.Is(err, ErrTimeout) {
		t.Fatalf("cancellation classified as timeout: result=%+v err=%v", result, err)
	}
}

func TestRunPipelineClassifiesStartDeadlineAsTimeout(t *testing.T) {
	t.Run("first_group", func(t *testing.T) {
		res, err := RunPipeline(t.Context(), []Stage{{Name: "sleep", Args: []string{"1"}}}, ExecOpts{
			Launch:  HostLaunch("start deadline test"),
			Timeout: time.Nanosecond,
		})
		var timeout *TimeoutError
		if !errors.As(err, &timeout) || !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("start deadline error = %v, want TimeoutError", err)
		}
		if res == nil || !res.TimedOut || !timeout.CommandDeadline {
			t.Fatalf("start deadline result = %+v, timeout = %+v", res, timeout)
		}
	})
	t.Run("second_group", func(t *testing.T) {
		calls := 0
		launch := HostLaunch("second group start deadline test")
		launch.CurrentAuthority = func(ctx context.Context) error {
			calls++
			if calls > 1 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}
		res, err := RunPipeline(t.Context(), []Stage{
			{Name: "true"},
			{Name: "true", Connector: argv.ConnectorAnd},
		}, ExecOpts{
			Launch:  launch,
			Timeout: 25 * time.Millisecond,
		})
		var timeout *TimeoutError
		if !errors.As(err, &timeout) || !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("second group start deadline error = %v, want TimeoutError", err)
		}
		if res == nil || !res.TimedOut || !timeout.CommandDeadline {
			t.Fatalf("second group start deadline result = %+v, timeout = %+v", res, timeout)
		}
	})
}

func TestRunPTYClassifiesStartDeadlineAsTimeout(t *testing.T) {
	_, code, err := RunPTY(t.Context(), "sleep", []string{"1"}, PTYOpts{
		Launch:  HostLaunch("pty start deadline test"),
		Timeout: time.Nanosecond,
	})
	if !errors.Is(err, ErrTimeout) || code != -1 {
		t.Fatalf("pty start deadline code=%d err=%v, want -1 and ErrTimeout", code, err)
	}
}
