package delegation

import (
	"context"
	"database/sql"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// RecordLegOutcome settles only the worker currently assigned to an active leg.
func (s *SQLStore) RecordLegOutcome(ctx context.Context, leg api.Leg) (bool, error) {
	changed := false
	err := s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		if _, err := qtx.GetDelegationLeg(ctx, db.GetDelegationLegParams{DelegationID: leg.DelegationID, ID: leg.ID}); err != nil {
			if db.IsNoRows(err) {
				return ErrLegNotFound
			}
			return err
		}
		if leg.WorkerID == "" {
			return nil
		}
		result, err := db.MarshalJSON(leg.Result)
		if err != nil {
			return err
		}
		n, err := qtx.RecordDelegationLegOutcome(ctx, db.RecordDelegationLegOutcomeParams{
			Status: string(leg.Status), ResultJson: result, CompletedAt: db.NullTimePtr(leg.CompletedAt),
			DelegationID: leg.DelegationID, ID: leg.ID, WorkerID: db.NullString(leg.WorkerID),
		})
		if err != nil {
			return err
		}
		changed = n == 1
		if changed {
			return s.emitDelegationTx(ctx, tx, leg.DelegationID, leg.ID)
		}
		return nil
	})
	return changed && err == nil, err
}
