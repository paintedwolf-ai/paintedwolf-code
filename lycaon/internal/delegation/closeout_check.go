package delegation

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// Terminal status is the authoritative delegation lifecycle state.
func delegationSettled(d *api.Delegation) bool {
	switch d.Status {
	case api.DelegationStatusDone, api.DelegationStatusFailed, api.DelegationStatusAborted, api.DelegationStatusCanceled:
		return true
	case api.DelegationStatusActive:
		return false
	}
	return false
}

// CloseoutComplete returns a condition callback: true when no delegation exists for the
// session or the linked delegation is settled.
func CloseoutComplete(store Store) func(ctx context.Context, sessionID string) (bool, error) {
	return func(ctx context.Context, sessionID string) (bool, error) {
		delegationID, ok := store.DelegationBySessionID(sessionID)
		if !ok {
			return true, nil
		}
		d, err := store.Get(ctx, delegationID)
		if err != nil {
			return false, err
		}
		return delegationSettled(d), nil
	}
}
