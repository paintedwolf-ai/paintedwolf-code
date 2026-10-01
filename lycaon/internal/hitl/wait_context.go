package hitl

import "context"

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
