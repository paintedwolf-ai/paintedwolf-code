package tools

import "context"

type commandReadKey struct{}

// WithCommandReadObserver binds a program-local observation of native file
// inputs. It records a read for notices, never evidence of model delivery.
func WithCommandReadObserver(ctx context.Context, observer func(context.Context, string, string) error) context.Context {
	return context.WithValue(ctx, commandReadKey{}, observer)
}

func ObserveCommandFileRead(ctx context.Context, rootID, path string) error {
	if observer, ok := ctx.Value(commandReadKey{}).(func(context.Context, string, string) error); ok {
		return observer(ctx, rootID, path)
	}
	return nil
}
