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

// CommitDeliveredSource persists a confirmed source delivery and its evidence.
func (s *SQL) CommitDeliveredSource(ctx context.Context, sessionID string, observation evidence.DeliveredSourceObservation) (string, error) {
	rec := observation.Record()
	if rec.SourceDelivery == nil {
		return "", fmt.Errorf("confirmed source delivery required")
	}
	unlock := s.lockMutation(sessionID)
	defer unlock()
	release := bloblifecycle.AcquirePublication(s.dataDir)
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	fact := rec.SourceDelivery
	q := s.queries.WithTx(tx)
	roots, err := evidenceCitationRoots(ctx, q, sessionID)
	if err != nil {
		return "", err
	}
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
	// Bind confirmed bytes to the session root for citation identity.
	if id := evidence.ResolvePathIdentity(roots, rec.Path); id.ScopeRel != "" {
		rec.Path = id.ScopeRel
		rec.RootID = id.RootID
	}
	evidence.StampDisplayKeys(roots, &rec)
	live, metaLedger, nextSeq, err := loadCommitLedgers(ctx, q, sessionID, roots, rec, rec.Path != "")
	if err != nil {
		return "", err
	}
	if nextSeq > 0 {
		rec.SetCustodySeq(nextSeq)
	}
	if rec.Path != "" {
		if urls := evidence.ExternalCustodyURLs(metaLedger, evidence.DisplayPathsForRecord(rec)); len(urls) > 0 {
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
	return rec.Handle, nil
}
