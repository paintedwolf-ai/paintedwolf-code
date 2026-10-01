//go:build unix

package exec

import (
	"context"
	"errors"
	"testing"
	"time"

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
			started := time.Now()
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
			if result == nil || !result.TimedOut || timeout.Elapsed <= 0 || timeout.Elapsed > time.Since(started) {
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
