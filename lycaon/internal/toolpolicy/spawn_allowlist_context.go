package toolpolicy

import "context"

type taskSpawnAllowlistKey struct{}

// WithTaskSpawnAllowlist attaches a turn-scoped task() agent allowlist to ctx for rule
// evaluation. An empty (non-nil) list is meaningful: the turn resolved a roster and
// filtered every agent out, so nothing is dispatchable. Only an absent key falls back
// to the declared workflow allowlist.
func WithTaskSpawnAllowlist(ctx context.Context, agents []string) context.Context {
	if ctx == nil {
		return nil
	}
	cp := append([]string{}, agents...)
	return context.WithValue(ctx, taskSpawnAllowlistKey{}, cp)
}

// TaskSpawnAllowlistFromContext returns the turn-scoped allowlist when one was attached.
// ok distinguishes attached-but-empty ("no agents dispatchable") from not attached.
func TaskSpawnAllowlistFromContext(ctx context.Context) ([]string, bool) {
	if ctx == nil {
		return nil, false
	}
	raw, ok := ctx.Value(taskSpawnAllowlistKey{}).([]string)
	if !ok {
		return nil, false
	}
	return append([]string{}, raw...), true
}
