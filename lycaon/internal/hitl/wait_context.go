package hitl

import (
	"context"
	"errors"
	"time"
)

type stopCtxKey struct{}

// WithStopContext attaches a session-stop context with no deadline.
func WithStopContext(ctx, stop context.Context) context.Context {
	if stop == nil {
		return ctx
	}
	return context.WithValue(ctx, stopCtxKey{}, stop)
}

// WaitContext returns the attached stop context, or ctx when none is set.
func WaitContext(ctx context.Context) context.Context {
	if stop, ok := ctx.Value(stopCtxKey{}).(context.Context); ok && stop != nil {
		return stop
	}
	return ctx
}

// IOContext bounds network I/O by timeout on the stop context, so time spent
// waiting for approval never consumes the caller's deadline. The caller's
// explicit cancellation still ends the I/O; only its deadline is set aside.
func IOContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	stop := WaitContext(ctx)
	io, cancel := context.WithTimeout(stop, timeout)
	if stop == ctx {
		return io, cancel
	}
	detach := context.AfterFunc(ctx, func() {
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			cancel()
		}
	})
	return io, func() {
		detach()
		cancel()
	}
}
