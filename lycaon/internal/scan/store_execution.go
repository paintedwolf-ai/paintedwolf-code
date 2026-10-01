package scan

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

// ClaimNext atomically claims the oldest pending scan for the runner.
func (s *SQLStore) ClaimNext(ctx context.Context) (*api.CodeScan, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)

	// Prioritize SCA and secrets when scan concurrency is limited.
	id, err := qtx.NextPendingScanID(ctx)
	if db.IsNoRows(err) {
		return nil, ErrNoPendingScans
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	n, err := qtx.ClaimPendingScan(ctx, db.ClaimPendingScanParams{
		ClaimedBy: db.NullString("local-scan-runner"), ClaimToken: db.NullString(uuid.NewString()),
		HeartbeatAt:    db.NullString(db.FormatTime(now)),
		LeaseExpiresAt: db.NullString(db.FormatTime(now.Add(ScanClaimLease))), ID: id,
	})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNoPendingScans
	}
	if err := s.emitScanTx(ctx, tx, id); err != nil {
		return nil, err
	}
	row, err := qtx.GetCodeScan(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load claimed scan: %w", err)
	}
	claimed := codeScanFromRow(row)
	if err := hydrateScanFacts(ctx, tx, claimed); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.notify()
	return claimed, nil
}

// CountRunning returns the number of in-flight scans.
func (s *SQLStore) CountRunning(ctx context.Context) (int, error) {
	n, err := s.queries.CountRunningScans(ctx)
	return int(n), err
}

func (s *SQLStore) SetExecutionPolicy(ctx context.Context, claimed *api.CodeScan, policy scancatalog.RuntimePolicy, startedAt time.Time) (bool, error) {
	if claimed == nil || strings.TrimSpace(claimed.ID) == "" || strings.TrimSpace(claimed.ClaimToken) == "" {
		return false, fmt.Errorf("scan claim token required")
	}
	norm := policy.Normalized()
	apiPolicy := api.ScanRuntimePolicy{
		SoftLimitMs: norm.SoftLimitSec * 1000,
		HardLimitMs: norm.HardLimitSec * 1000,
		CPUUnits:    norm.CPUUnits,
		Parallelism: norm.Parallelism,
	}
	raw, err := json.Marshal(apiPolicy)
	if err != nil {
		return false, err
	}
	return s.casInTx(ctx, claimed.ID, func(qtx *db.Queries) (int64, error) {
		return qtx.SetScanExecutionPolicy(ctx, db.SetScanExecutionPolicyParams{
			RuntimeJson: string(raw), StartedAt: db.NullString(db.FormatTime(startedAt.UTC())),
			ID: claimed.ID, ClaimToken: db.NullString(claimed.ClaimToken),
		})
	})
}

func (s *SQLStore) MarkLongRunning(ctx context.Context, claimed *api.CodeScan, at time.Time) (bool, error) {
	if claimed == nil || strings.TrimSpace(claimed.ID) == "" || strings.TrimSpace(claimed.ClaimToken) == "" {
		return false, fmt.Errorf("scan claim token required")
	}
	return s.casInTx(ctx, claimed.ID, func(qtx *db.Queries) (int64, error) {
		return qtx.MarkScanLongRunning(ctx, db.MarkScanLongRunningParams{
			LongRunningAt: db.NullString(db.FormatTime(at.UTC())), ID: claimed.ID,
			ClaimToken: db.NullString(claimed.ClaimToken),
		})
	})
}

// MarkProgress commits progress, summary, and event only for the active claim.
func (s *SQLStore) MarkProgress(ctx context.Context, claimed *api.CodeScan, progress api.ScanProgress) error {
	if claimed == nil || strings.TrimSpace(claimed.ID) == "" || strings.TrimSpace(claimed.ClaimToken) == "" {
		return fmt.Errorf("scan claim token required")
	}
	raw, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	won, err := s.casInTx(ctx, claimed.ID, func(qtx *db.Queries) (int64, error) {
		return qtx.SetScanProgress(ctx, db.SetScanProgressParams{
			ProgressJson: string(raw), ID: claimed.ID, ClaimToken: db.NullString(claimed.ClaimToken),
		})
	})
	if err == nil && won {
		claimed.Progress = &progress
	}
	return err
}

// MarkDelta records what a path-scoped scan changed against its base.
func (s *SQLStore) MarkDelta(ctx context.Context, claimed *api.CodeScan, delta api.ScanDelta) error {
	if claimed == nil || strings.TrimSpace(claimed.ID) == "" || strings.TrimSpace(claimed.ClaimToken) == "" {
		return fmt.Errorf("scan claim token required")
	}
	raw, err := json.Marshal(delta)
	if err != nil {
		return err
	}
	_, err = s.queries.SetScanDelta(ctx, db.SetScanDeltaParams{
		DeltaJson: string(raw), ID: claimed.ID, ClaimToken: db.NullString(claimed.ClaimToken),
	})
	if err == nil {
		claimed.Delta = &delta
	}
	return err
}

// BaseSnapshotID is the generation a path-scoped scan's targets were diffed
// from; empty for a full pass or a caller-supplied path list.
func (s *SQLStore) BaseSnapshotID(ctx context.Context, scanID string) (string, error) {
	base, err := s.queries.GetScanBaseSnapshot(ctx, strings.TrimSpace(scanID))
	if db.IsNoRows(err) {
		return "", nil
	}
	return base, err
}

// MarkComplete atomically commits a result for the active claim.
func (s *SQLStore) MarkComplete(ctx context.Context, claimed *api.CodeScan, result *scanoutput.Result) (bool, error) {
	if claimed == nil || strings.TrimSpace(claimed.ID) == "" || strings.TrimSpace(claimed.ClaimToken) == "" {
		return false, fmt.Errorf("scan claim token required")
	}
	if result == nil {
		result = &scanoutput.Result{}
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	return s.FinalizeCompleteJSON(
		ctx,
		claimed,
		resultJSON,
		result.Findings,
		result.Warnings,
		scanoutput.CoverageForResult(result, claimed.SourceCaptureQuality, claimed.SourceAdmissionMode),
	)
}

// MarkPendingSuperseded redirects consumers to compatible evidence.
func (s *SQLStore) MarkPendingSuperseded(ctx context.Context, id, replacementScanID string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, fmt.Errorf("scan id required")
	}
	replacementScanID = strings.TrimSpace(replacementScanID)
	if replacementScanID == "" {
		return false, fmt.Errorf("replacement scan id required")
	}
	won := false
	err := s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		source, err := qtx.GetCodeScan(ctx, id)
		if err != nil {
			return err
		}
		replacement, err := qtx.GetCodeScan(ctx, replacementScanID)
		if err != nil {
			return fmt.Errorf("load replacement scan %s: %w", replacementScanID, err)
		}
		if id == replacementScanID || source.CanonicalPath != replacement.CanonicalPath ||
			source.ScannerID != replacement.ScannerID || source.ReuseKey != replacement.ReuseKey ||
			(source.SourceSnapshotID != api.SourceSnapshotWarming && source.SourceSnapshotID != replacement.SourceSnapshotID) {
			return fmt.Errorf("replacement scan %s is not execution-compatible with %s", replacementScanID, id)
		}
		changed, err := qtx.MarkPendingScanSuperseded(ctx, db.MarkPendingScanSupersededParams{
			Error:             db.NullString(scanSupersededMessage),
			ReplacementScanID: replacementScanID,
			CompletedAt:       db.NullString(db.FormatTime(time.Now().UTC())),
			ID:                id,
		})
		if err != nil || changed != 1 {
			return err
		}
		if err := qtx.SetScanRunFailure(ctx, db.SetScanRunFailureParams{
			CoverageStatus: string(api.ScanCoverageUnavailable),
			FailureCode:    "SCAN_SUPERSEDED", ScanID: id,
		}); err != nil {
			return err
		}
		if err := qtx.TransferWorkflowScanBindings(ctx, db.TransferWorkflowScanBindingsParams{
			ReplacementID: replacementScanID, SourceID: id,
		}); err != nil {
			return err
		}
		if err := qtx.TransferSessionScanBindings(ctx, db.TransferSessionScanBindingsParams{
			ReplacementID: replacementScanID, SourceID: id,
		}); err != nil {
			return err
		}
		if err := qtx.TransferAssessmentScanBindings(ctx, db.TransferAssessmentScanBindingsParams{
			ReplacementID: replacementScanID, SourceID: id,
		}); err != nil {
			return err
		}
		if replacement.SourceSnapshotID != api.SourceSnapshotWarming {
			if err := qtx.BindPublishedAssessmentSnapshot(ctx, db.BindPublishedAssessmentSnapshotParams{
				SourceSnapshotID: replacement.SourceSnapshotID,
				ScanID:           replacementScanID,
			}); err != nil {
				return err
			}
		}
		if err := qtx.DeleteWorkflowScanBindings(ctx, id); err != nil {
			return err
		}
		if err := qtx.DeleteSessionScanBindings(ctx, id); err != nil {
			return err
		}
		if err := qtx.DeleteAssessmentScanBindings(ctx, id); err != nil {
			return err
		}
		won = true
		if err := s.emitScanTx(ctx, tx, replacementScanID); err != nil {
			return err
		}
		return s.emitScanTx(ctx, tx, id)
	})
	return won && err == nil, err
}

const (
	FailurePathsUnavailable  = "SCAN_PATHS_UNAVAILABLE"
	FailureDefinitionChanged = "SCAN_DEFINITION_CHANGED"
	FailureSourceUnavailable = "SCAN_SOURCE_UNAVAILABLE"
	FailureEngine            = "SCAN_ENGINE_FAILED"
	FailureTimeout           = "SCAN_ENGINE_TIMEOUT"
	FailureIngest            = "SCAN_INGEST_FAILED"
	FailureCommit            = "SCAN_RESULT_COMMIT_FAILED"
)
