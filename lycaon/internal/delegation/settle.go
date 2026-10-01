package delegation

import (
	"context"
	"database/sql"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

func (s *SQLStore) Settle(ctx context.Context, delegationID string) (bool, error) {
	changed := false
	err := s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		if _, err := qtx.GetDelegation(ctx, delegationID); err != nil {
			if db.IsNoRows(err) {
				return ErrDelegationNotFound
			}
			return err
		}
		n, err := qtx.SettleDelegation(ctx, delegationID)
		if err != nil {
			return err
		}
		changed = n == 1
		if changed {
			return s.emitDelegationTx(ctx, tx, delegationID, "")
		}
		return nil
	})
	return changed && err == nil, err
}

func (s *MemoryStore) Settle(ctx context.Context, delegationID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.delegations[delegationID]
	if !ok {
		return false, ErrDelegationNotFound
	}
	if delegationSettled(&rec.delegation) {
		return false, nil
	}
	status := api.DelegationStatusDone
	for _, leg := range rec.legs {
		if !leg.Status.IsTerminal() {
			return false, nil
		}
		if leg.Status == api.LegStatusFailed {
			status = api.DelegationStatusFailed
		}
		if leg.Status == api.LegStatusCanceled && status != api.DelegationStatusFailed {
			status = api.DelegationStatusCanceled
		}
	}
	rec.delegation.Status = status
	rec.delegation.Phase = api.DelegationPhaseDone
	return true, nil
}
