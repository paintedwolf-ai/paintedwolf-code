package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/db"
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
	q := s.queries.WithTx(tx)
	existing, err := q.FindSourceDeliveryHandle(ctx, db.FindSourceDeliveryHandleParams{
		SessionID: sessionID, ResponseID: fact.ResponseID, DeliveryID: fact.DeliveryID,
		SourceSha256: fact.Source.SHA256, SourceID: fact.Source.SourceID, SourceRevision: fact.Source.Revision,
		SourceStartByte: fact.SourceStartByte, SourceEndByte: fact.SourceEndByte,
	})
	if err == nil {
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	projectID, err := q.GetSessionProjectID(ctx, sessionID)
	if err != nil {
		return "", err
	}
	ordinal, err := nextEvidenceOrdinal(ctx, q, sessionID, rec.Kind)
	if err != nil {
		return "", err
	}
	rec.Handle = evidence.FormatHandle(rec.Kind, ordinal)
	live, metaLedger, err := loadCommitLedgers(ctx, q, sessionID, rec, rec.Path != "")
	if err != nil {
		return "", err
	}
	if rec.Path != "" {
		if urls := evidence.ExternalCustodyURLs(metaLedger, []string{rec.Path}); len(urls) > 0 {
			evidence.ApplyExternalCustody(&rec, urls)
		}
	}
	if err := supersedePathsForRecord(ctx, q, sessionID, rec.Handle, rec, live); err != nil {
		return "", err
	}
	if _, err = writeEvidenceRecord(ctx, q, s.dataDir, sessionID, projectID, rec); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	bumpUntrusted(s.sqlCore, sessionID, rec)
	bumpSecretExposure(s.sqlCore, sessionID, rec)
	if err := s.projectUntrustedLedger(ctx, nil, sessionID, rec); err != nil {
		return "", err
	}
	return rec.Handle, nil
}

// CommitDeliveredSource records a confirmed exact delivered page in memory.
func (ev *memoryEvidence) CommitDeliveredSource(ctx context.Context, sessionID string, observation evidence.DeliveredSourceObservation) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	rec := observation.Record()
	if rec.SourceDelivery == nil {
		return "", fmt.Errorf("confirmed source delivery required")
	}
	s := ev.core
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return "", ErrSessionNotFound
	}
	fact := rec.SourceDelivery
	liveRows := ensureEvidenceSession(s, sessionID)
	for handle, row := range liveRows {
		if existing := row.rec.SourceDelivery; existing != nil &&
			existing.ResponseID == fact.ResponseID &&
			existing.DeliveryID == fact.DeliveryID &&
			existing.Source.SHA256 == fact.Source.SHA256 &&
			existing.Source.SourceID == fact.Source.SourceID &&
			existing.Source.Revision == fact.Source.Revision &&
			existing.SourceStartByte == fact.SourceStartByte &&
			existing.SourceEndByte == fact.SourceEndByte {
			return handle, nil
		}
	}
	liveRecords := collectMemoryEvidenceRecordsLocked(liveRows, true)
	live := evidence.AssembleLedger(liveRecords)
	if rec.Path != "" {
		if urls := evidence.ExternalCustodyURLs(live, []string{rec.Path}); len(urls) > 0 {
			evidence.ApplyExternalCustody(&rec, urls)
		}
	}
	rec.Handle = evidence.FormatHandle(rec.Kind, nextEvidenceOrdinalLocked(s, sessionID, rec.Kind))
	for _, path := range evidence.IndexedPathsForRecord(rec) {
		for _, oldHandle := range live.ByPath[path] {
			if oldHandle == rec.Handle {
				continue
			}
			row, ok := liveRows[oldHandle]
			if !ok || row.supersededBy != "" {
				continue
			}
			if !evidence.SupersedesPathHandle(rec, row.rec) {
				continue
			}
			row.supersededBy = rec.Handle
			liveRows[oldHandle] = row
		}
	}
	putMemoryEvidenceRowLocked(s, sessionID, rec.Handle, rec)
	bumpUntrustedLocked(s, sessionID, rec)
	bumpSecretExposureLocked(s, sessionID, rec)
	return rec.Handle, nil
}

