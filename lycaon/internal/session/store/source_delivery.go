package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/evidence"
)

// CommitDeliveredSource records a confirmed exact delivered page through the
// existing evidence owner. It never calls RememberAgentRead or accepts stdout.
func (s *SQL) CommitDeliveredSource(ctx context.Context, sessionID string, observation evidence.DeliveredSourceObservation) (string, error) {
	rec := observation.Record()
	if rec.SourceDelivery == nil {
		return "", fmt.Errorf("confirmed source delivery required")
	}
	release := bloblifecycle.AcquirePublication(s.dataDir)
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	fact := rec.SourceDelivery
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT handle FROM evidence_records WHERE session_id=? AND json_extract(line_ranges,'$.source_delivery.response_id')=? AND json_extract(line_ranges,'$.source_delivery.delivery_id')=? AND json_extract(line_ranges,'$.source_delivery.source.SHA256')=? AND json_extract(line_ranges,'$.source_delivery.source.SourceID')=? AND json_extract(line_ranges,'$.source_delivery.source.Revision')=? AND json_extract(line_ranges,'$.source_delivery.source_start_byte')=? AND json_extract(line_ranges,'$.source_delivery.source_end_byte')=?`, sessionID, fact.ResponseID, fact.DeliveryID, fact.Source.SHA256, fact.Source.SourceID, fact.Source.Revision, fact.SourceStartByte, fact.SourceEndByte).Scan(&existing)
	if err == nil {
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	q := s.queries.WithTx(tx)
	projectID, err := q.GetSessionProjectID(ctx, sessionID)
	if err != nil {
		return "", err
	}
	ordinal, err := nextEvidenceOrdinal(ctx, q, sessionID, rec.Kind)
	if err != nil {
		return "", err
	}
	rec.Handle = evidence.FormatHandle(rec.Kind, ordinal)
	live, err := loadFileBindings(ctx, q, sessionID, rec)
	if err != nil {
		return "", err
	}
	if err = s.supersedePathsForRecord(ctx, q, sessionID, rec.Handle, rec, live); err != nil {
		return "", err
	}
	if _, err = writeEvidenceRecord(ctx, q, s.dataDir, sessionID, projectID, rec); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	s.bumpUntrusted(sessionID, rec)
	s.bumpSecretExposure(sessionID, rec)
	if err = s.projectUntrustedLedger(ctx, nil, sessionID, rec); err != nil {
		return "", err
	}
	return rec.Handle, nil
}
