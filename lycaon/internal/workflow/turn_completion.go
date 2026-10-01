package workflow

import (
	"context"
)

// ReconcileTurnCompletion evaluates completion after coordinator work.
func (m *RunManager) ReconcileTurnCompletion(ctx context.Context, sessionID string) error {
	if m == nil {
		return nil
	}
	run, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil || IsTerminal(run.Status) {
		return err
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return err
	}
	_, err = m.TryAutoAdvanceThroughCommittedGates(ctx, run.ID, len(manifest.Phases)+1)
	return err
}
