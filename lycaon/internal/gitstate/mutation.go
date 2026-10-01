package gitstate

import "context"

// MutationObserver brackets a live Git effect while its repository lease is held.
// Finishing records observations even when execution failed or was canceled.
type MutationObserver func(context.Context, string, []string) (func(context.Context) error, error)

type mutationObserverKey struct{}

func WithMutationObserver(ctx context.Context, observer MutationObserver) context.Context {
	return context.WithValue(ctx, mutationObserverKey{}, observer)
}

func BeginMutation(ctx context.Context, dir string, paths []string) (func(context.Context) error, error) {
	if observer, ok := ctx.Value(mutationObserverKey{}).(MutationObserver); ok {
		return observer(ctx, dir, paths)
	}
	return func(context.Context) error { return nil }, nil
}
