package progress

import "context"

// AdoptActiveRun preserves an unbound plan and resets items from a prior workflow run.
func AdoptActiveRun(ctx context.Context, store Store, sessionID, runID string) (bool, error) {
	if store == nil {
		return false, nil
	}
	return store.BindRun(ctx, sessionID, runID, false)
}

// EnsureBootstrap starts a fresh plan after completion or a workflow run change.
func EnsureBootstrap(ctx context.Context, store Store, sessionID, runID string) (bool, error) {
	if store == nil {
		return false, nil
	}
	return store.BindRun(ctx, sessionID, runID, true)
}
