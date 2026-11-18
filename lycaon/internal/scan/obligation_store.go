package scan

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
)

// LandedChange links one committed source generation to its scan.
type LandedChange struct {
	ID            string
	WorkerJobID   string
	CanonicalPath string
	DelegationID  string
	WorkflowRunID string
	ChangedPaths  []string
	DeletedPaths  []string
	ScanRequired  bool
	ScanID        string
	CreatedAt     time.Time
}

// WarmingObligationIDs returns durable scans still waiting for snapshot publication.
func (s *RunStore) WarmingObligationIDs(ctx context.Context, limit int) ([]string, error) {
	if s == nil || s.tx.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 16
	}
	return s.tx.queries.WarmingObligationIDs(ctx, int64(limit))
}

// LandedChangeForScan returns exact attribution metadata for ingestion.
func (s *RunStore) LandedChangeForScan(ctx context.Context, scanID string) (*LandedChange, error) {
	if s == nil || s.tx.db == nil || strings.TrimSpace(scanID) == "" {
		return nil, nil
	}
	row, err := s.tx.queries.GetLandedChangeForScan(ctx, db.NullString(scanID))
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return landedChangeFromRow(row)
}

// LatestRequiredLandedChangeForSession returns the newest required generation landed by sessionID's workers.
func (s *RunStore) LatestRequiredLandedChangeForSession(ctx context.Context, sessionID string) (*LandedChange, error) {
	if s == nil || s.tx.db == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	row, err := s.tx.queries.LatestRequiredLandedChangeForSession(ctx, db.NullString(sessionID))
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return landedChangeFromRow(row)
}

// LatestRequiredScanForSession returns the cumulative landing scan for sessionID.
func (s *RunStore) LatestRequiredScanForSession(ctx context.Context, sessionID string) (*api.CodeScan, error) {
	landed, err := s.LatestRequiredLandedChangeForSession(ctx, sessionID)
	if err != nil || landed == nil {
		return nil, err
	}
	if strings.TrimSpace(landed.ScanID) == "" {
		return nil, fmt.Errorf("landed change %s is missing its scan obligation", landed.ID)
	}
	rec, err := s.reads.Get(ctx, landed.ScanID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("landed change %s references missing scan %s", landed.ID, landed.ScanID)
	}
	return rec, nil
}

func landedChangeFromRow(row db.LandedChanges) (*LandedChange, error) {
	out := LandedChange{
		ID: row.ID, WorkerJobID: row.WorkerJobID, CanonicalPath: row.CanonicalPath,
		DelegationID: row.DelegationID, WorkflowRunID: row.WorkflowRunID,
		ScanRequired: row.ScanRequired != 0, ScanID: db.StringFromNull(row.ScanID),
	}
	if err := json.Unmarshal([]byte(row.ChangedPathsJson), &out.ChangedPaths); err != nil {
		return nil, fmt.Errorf("decode landed changed paths: %w", err)
	}
	if err := json.Unmarshal([]byte(row.DeletedPathsJson), &out.DeletedPaths); err != nil {
		return nil, fmt.Errorf("decode landed deleted paths: %w", err)
	}
	out.CreatedAt, _ = time.Parse(time.RFC3339Nano, row.CreatedAt)
	return &out, nil
}

// CancelPendingScans makes the product main authoritative for queued work.
func (s *RunStore) CancelPendingScans(ctx context.Context, reason string) ([]api.CodeScan, error) {
	if s == nil || s.tx.db == nil {
		return nil, nil
	}
	var canceled []api.CodeScan
	err := db.InTx(ctx, s.tx.db, s.tx.queries, s.tx.notify, func(qtx *db.Queries, tx *sql.Tx) error {
		ids, err := qtx.CancelPendingScans(ctx, db.CancelPendingScansParams{
			Error: db.NullString(reason), CompletedAt: db.NullString(timelayout.Format(time.Now().UTC())),
		})
		if err != nil {
			return err
		}
		canceled = make([]api.CodeScan, 0, len(ids))
		for _, id := range ids {
			if err := qtx.SetScanRunFailure(ctx, db.SetScanRunFailureParams{
				CoverageStatus: string(api.ScanCoverageUnavailable),
				FailureCode:    "SCAN_CANCELED", ScanID: id,
			}); err != nil {
				return err
			}
			row, getErr := qtx.GetCodeScan(ctx, id)
			if getErr != nil {
				return getErr
			}
			canceled = append(canceled, *codeScanFromRow(row))
			if err := s.tx.emitScanTx(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && len(canceled) > 0 {
		s.tx.queueChanged.Notify()
		s.tx.seriesChanged.Notify()
	}
	return canceled, err
}

// bindHeadSHA updates provenance after the filesystem commit and snapshot publication.
func (s *RunStore) bindHeadSHA(ctx context.Context, scanID, headSHA string) error {
	if s == nil || s.tx.db == nil || strings.TrimSpace(headSHA) == "" {
		return nil
	}
	return s.tx.queries.BindScanHeadSHA(ctx, db.BindScanHeadSHAParams{HeadSha: headSHA, ID: scanID})
}
