package project

import "context"

type lifecycleForceKey struct{}

// WithForcedLifecycle marks a lifecycle call whose dependents were explicitly
// inventoried, canceled, and drained by the API boundary.
func WithForcedLifecycle(ctx context.Context) context.Context {
	return context.WithValue(ctx, lifecycleForceKey{}, true)
}

func forcedLifecycle(ctx context.Context) bool {
	forced, _ := ctx.Value(lifecycleForceKey{}).(bool)
	return forced
}
