package sourceledger

import (
	"context"
	"database/sql"

	"github.com/lycaon/lycaon/internal/sourcebranch"
)

// MutationScopeProvider owns the prepared filesystem operations a pass must defer.
// The observation transaction binds this fact to the head compare-and-set.
type MutationScopeProvider interface {
	ObservationScope(context.Context, *sql.Tx, string) (MutationObservationScope, error)
}

func (s *Store) SetMutationScopeProvider(provider MutationScopeProvider) {
	s.recordMu.Lock()
	s.mutationScopes = provider
	s.recordMu.Unlock()
}

// MutationObservationScope is a transaction-bound projection of pending paths.
type MutationObservationScope interface {
	Pending(sourcebranch.ID, string, string) bool
}

func (s *Store) mutationObservationScope(ctx context.Context, tx *sql.Tx, projectID string) (MutationObservationScope, error) {
	if s.mutationScopes == nil {
		return nil, nil
	}
	return s.mutationScopes.ObservationScope(ctx, tx, projectID)
}
