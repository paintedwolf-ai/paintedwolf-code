package scan

import (
	"context"
	"database/sql"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

const ScanClaimLease = 45 * time.Second

func (s *SQLStore) RenewClaim(ctx context.Context, scanID, claimToken string) (bool, error) {
	now := time.Now().UTC()
	rows, err := s.queries.RenewScanClaim(ctx, db.RenewScanClaimParams{
		HeartbeatAt: db.NullString(db.FormatTime(now)), LeaseExpiresAt: db.NullString(db.FormatTime(now.Add(ScanClaimLease))),
		ID: scanID, ClaimToken: db.NullString(claimToken),
	})
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

// RecoverExpiredClaims fails scans whose leases expired.
func (s *SQLStore) RecoverExpiredClaims(ctx context.Context) ([]api.CodeScan, error) {
	now := db.FormatTime(time.Now().UTC())
	ids, err := s.queries.ListExpiredScanIDs(ctx, db.NullString(now))
	if err != nil {
		return nil, err
	}
	var recovered []api.CodeScan
	for _, id := range ids {
		won := false
		err := s.inTx(ctx, func(q *db.Queries, tx *sql.Tx) error {
			changed, err := q.FailExpiredScanClaim(ctx, db.FailExpiredScanClaimParams{
				Error: db.NullString("scan interrupted after its host lease expired"), CompletedAt: db.NullString(now),
				ID: id, ExpiredBefore: db.NullString(now),
			})
			if err != nil || changed != 1 {
				return err
			}
			if err := q.SetScanRunFailure(ctx, db.SetScanRunFailureParams{
				CoverageStatus: string(api.ScanCoverageUnavailable),
				FailureCode:    "SCAN_LEASE_EXPIRED", ScanID: id,
			}); err != nil {
				return err
			}
			won = true
			return s.emitScanTx(ctx, tx, id)
		})
		if err != nil {
			return nil, err
		}
		if won {
			if scan, err := s.Get(ctx, id); err != nil {
				return nil, err
			} else if scan != nil {
				recovered = append(recovered, *scan)
			}
		}
	}
	return recovered, nil
}

func (s *SQLStore) NextLeaseExpiry(ctx context.Context) (time.Time, error) {
	raw, err := s.queries.NextScanLeaseExpiry(ctx)
	return parseCadenceTime(raw), err
}
