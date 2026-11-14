package hitl

import "context"

// HumanWaitObserver learns when work under a context waits on a person, so
// that time is not counted as the work running.
type HumanWaitObserver interface {
	// BeginHumanWait marks a wait's start; the returned func marks its end.
	BeginHumanWait() (end func())
}

type humanWaitKey struct{}

// WithHumanWaitObserver routes checkpoint waits under ctx to observer.
func WithHumanWaitObserver(ctx context.Context, observer HumanWaitObserver) context.Context {
	return context.WithValue(ctx, humanWaitKey{}, observer)
}

func beginHumanWait(ctx context.Context) (end func()) {
	if observer, ok := ctx.Value(humanWaitKey{}).(HumanWaitObserver); ok && observer != nil {
		return observer.BeginHumanWait()
	}
	return func() {}
}
