package phases

import (
	"context"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

// ReconcileTurnCompletion evaluates completion after coordinator work.
func (m *Service) ReconcileTurnCompletion(ctx context.Context, sessionID string) error {
	if m == nil {
		return nil
	}
	run, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil || runstate.IsTerminal(run.Status) {
		return err
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return err
	}
	_, err = m.TryAutoAdvanceThroughCommittedGates(ctx, run.ID, len(manifest.Phases)+1)
	return err
}
