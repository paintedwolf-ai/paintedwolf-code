package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

// ErrPresentationMismatch identifies a stale presentation target.
var ErrPresentationMismatch = errors.New("presentation effect does not match displayed file")

// ErrPresentationNotFound identifies a file with no completed look.
var ErrPresentationNotFound = errors.New("file has no completed presentation")

// CompletePresentation acknowledges all file changes through the displayed effect.
func (s *Checkpoints) CompletePresentation(
	ctx context.Context,
	projectID, fileID, effectID string,
	ordinal int64,
) error {
	if s == nil {
		return fmt.Errorf("ledger not configured")
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if err := completePresentationQueries(
		ctx, q, projectID, fileID, effectID, ordinal, ts,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func completePresentationQueries(
	ctx context.Context,
	q *db.Queries,
	projectID, fileID, effectID string,
	ordinal int64,
	ts string,
) error {
	row, err := q.GetSourceEffect(ctx, effectID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPresentationMismatch
	}
	if err != nil {
		return err
	}
	operation, err := q.GetSourceOperation(ctx, row.OperationID)
	if err != nil {
		return err
	}
	if row.ProjectID != projectID || row.FileID != fileID || row.Ordinal != ordinal || row.WalkVisible == 0 ||
		sourcebranch.ID(operation.BranchID).IsWorker() {
		return ErrPresentationMismatch
	}
	if err := q.AdvanceSourcePresentationWatermark(ctx, db.AdvanceSourcePresentationWatermarkParams{
		ProjectID: projectID, FileID: fileID,
		ThroughOrdinal: ordinal, DisplayedEffectID: effectID, SeenTs: ts,
	}); err != nil {
		return err
	}
	return q.CompleteSourceAgentPresentationsThrough(ctx, db.CompleteSourceAgentPresentationsThroughParams{
		ProjectID: projectID, FileID: fileID, Ordinal: ordinal,
	})
}

// WithdrawPresentation restores the watermark before the latest look.
// through guards against withdrawing a newer look.
func (s *Checkpoints) WithdrawPresentation(
	ctx context.Context,
	projectID, fileID string,
	through int64,
) error {
	if s == nil {
		return fmt.Errorf("ledger not configured")
	}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	watermark, err := q.GetSourcePresentationWatermark(ctx, db.GetSourcePresentationWatermarkParams{
		ProjectID: projectID, FileID: fileID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPresentationNotFound
	}
	if err != nil {
		return err
	}
	if watermark.ThroughOrdinal != through {
		return ErrPresentationMismatch
	}
	if watermark.SeenAfterOrdinal == watermark.ThroughOrdinal {
		return ErrPresentationNotFound
	}
	if err := rewindWatermark(ctx, q, projectID, fileID, watermark.SeenAfterOrdinal); err != nil {
		return err
	}
	if err := q.RequeueSourceAgentPresentations(ctx, db.RequeueSourceAgentPresentationsParams{
		ProjectID: projectID, FileID: fileID,
		AfterOrdinal: watermark.SeenAfterOrdinal, ThroughOrdinal: watermark.ThroughOrdinal,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// A zero or missing prior effect clears the watermark.
func rewindWatermark(ctx context.Context, q *db.Queries, projectID, fileID string, ordinal int64) error {
	drop := db.DeleteSourcePresentationWatermarkParams{ProjectID: projectID, FileID: fileID}
	if ordinal == 0 {
		return q.DeleteSourcePresentationWatermark(ctx, drop)
	}
	effectID, err := q.GetSourceEffectIDForFileAtOrdinal(ctx, db.GetSourceEffectIDForFileAtOrdinalParams{
		ProjectID: projectID, FileID: fileID, Ordinal: ordinal,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return q.DeleteSourcePresentationWatermark(ctx, drop)
	}
	if err != nil {
		return err
	}
	return q.WithdrawSourcePresentationWatermark(ctx, db.WithdrawSourcePresentationWatermarkParams{
		DisplayedEffectID: effectID, ProjectID: projectID, FileID: fileID,
	})
}
