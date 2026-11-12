package store

import (
	"context"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

func (ev *sqlEvidence) CommitProgramObservation(ctx context.Context, sessionID, tool string, receipt *api.CommandProgramReceipt, content string) (string, error) {
	rec := evidence.BuildProgramObservation(tool, receipt, content)
	if rec.Kind == "" {
		return "", nil
	}
	s := ev.core
	unlock := s.lockMutation(sessionID)
	defer unlock()
	release := bloblifecycle.AcquirePublication(s.dataDir)
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
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
	if _, err := writeEvidenceRecord(ctx, q, s.dataDir, sessionID, projectID, rec); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	bumpUntrusted(s, sessionID, rec)
	bumpSecretExposure(s, sessionID, rec)
	if err := s.projectUntrustedLedger(ctx, nil, sessionID, rec); err != nil {
		return "", err
	}
	return rec.Handle, nil
}

func (ev *memoryEvidence) CommitProgramObservation(ctx context.Context, sessionID, tool string, receipt *api.CommandProgramReceipt, content string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	rec := evidence.BuildProgramObservation(tool, receipt, content)
	if rec.Kind == "" {
		return "", nil
	}
	s := ev.core
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return "", ErrSessionNotFound
	}
	rec.Handle = evidence.FormatHandle(rec.Kind, nextEvidenceOrdinalLocked(s, sessionID, rec.Kind))
	putMemoryEvidenceRowLocked(s, sessionID, rec.Handle, rec)
	bumpUntrustedLocked(s, sessionID, rec)
	bumpSecretExposureLocked(s, sessionID, rec)
	return rec.Handle, nil
}

// programProvenanceRecords are the records one command program's network
// reads produce, in custody order: what it received, the directories it
// filled, and the external-custody paths it replaced locally.
func programProvenanceRecords(projectDir string, prov evidence.ProgramHTTPProvenance) []evidence.Record {
	var recs []evidence.Record
	for _, rec := range []evidence.Record{
		evidence.BuildProgramHTTPRecord(projectDir, prov),
		evidence.BuildProgramTreeRecord(projectDir, prov),
		evidence.BuildProgramCustodyReplacementRecord(projectDir, prov),
	} {
		if rec.Kind != "" {
			recs = append(recs, rec)
		}
	}
	return recs
}

// CommitProgramHTTPProvenance persists what a command program received from
// the network, the directories it filled, and the custody it replaced.
func (ev *sqlEvidence) CommitProgramHTTPProvenance(ctx context.Context, sessionID, projectDir string, prov evidence.ProgramHTTPProvenance) (string, error) {
	recs := programProvenanceRecords(projectDir, prov)
	if len(recs) == 0 {
		return "", nil
	}
	s := ev.core
	unlock := s.lockMutation(sessionID)
	defer unlock()
	release := bloblifecycle.AcquirePublication(s.dataDir)
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	projectID, err := q.GetSessionProjectID(ctx, sessionID)
	if err != nil {
		return "", err
	}
	nextSeq := 0
	var inserted []evidence.Record
	for i := range recs {
		rec := &recs[i]
		ordinal, err := nextEvidenceOrdinal(ctx, q, sessionID, rec.Kind)
		if err != nil {
			return "", err
		}
		rec.Handle = evidence.FormatHandle(rec.Kind, ordinal)
		if evidence.ReplacesWholeFileCustody(*rec) && rec.CustodySeq() == 0 {
			if nextSeq == 0 {
				if nextSeq, err = nextCustodySeq(ctx, q, sessionID); err != nil {
					return "", err
				}
			}
			rec.SetCustodySeq(nextSeq)
			nextSeq++
		}
		ok, err := writeEvidenceRecord(ctx, q, s.dataDir, sessionID, projectID, *rec)
		if err != nil {
			return "", err
		}
		if ok {
			inserted = append(inserted, *rec)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	for _, rec := range inserted {
		bumpUntrusted(s, sessionID, rec)
		bumpSecretExposure(s, sessionID, rec)
		if err := s.projectUntrustedLedger(ctx, nil, sessionID, rec); err != nil {
			return "", err
		}
	}
	return recs[0].Handle, nil
}

func (ev *memoryEvidence) CommitProgramHTTPProvenance(ctx context.Context, sessionID, projectDir string, prov evidence.ProgramHTTPProvenance) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	recs := programProvenanceRecords(projectDir, prov)
	if len(recs) == 0 {
		return "", nil
	}
	s := ev.core
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return "", ErrSessionNotFound
	}
	for i := range recs {
		rec := &recs[i]
		rec.Handle = evidence.FormatHandle(rec.Kind, nextEvidenceOrdinalLocked(s, sessionID, rec.Kind))
		putMemoryEvidenceRowLocked(s, sessionID, rec.Handle, *rec)
		bumpUntrustedLocked(s, sessionID, *rec)
		bumpSecretExposureLocked(s, sessionID, *rec)
	}
	return recs[0].Handle, nil
}

// ExternalCustodyURLsForPaths returns external URLs associated with any of paths in sessionID.
func (ev *sqlEvidence) ExternalCustodyURLsForPaths(ctx context.Context, sessionID, projectDir string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	metaLedger, err := loadMetadataLedger(ctx, ev.core.queries, sessionID)
	if err != nil {
		return nil, err
	}
	normalized := make([]string, 0, len(paths))
	for _, p := range paths {
		if rel, ok := evidence.NormalizeCitationPath(projectDir, p); ok {
			normalized = append(normalized, rel)
		} else {
			normalized = append(normalized, evidence.NormalizeLedgerPath(p))
		}
	}
	return evidence.ExternalCustodyURLs(metaLedger, normalized), nil
}

func (ev *memoryEvidence) ExternalCustodyURLsForPaths(ctx context.Context, sessionID, projectDir string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	ledger, err := loadLedgerMemory(ctx, ev.core, sessionID)
	if err != nil {
		return nil, err
	}
	normalized := make([]string, 0, len(paths))
	for _, p := range paths {
		if rel, ok := evidence.NormalizeCitationPath(projectDir, p); ok {
			normalized = append(normalized, rel)
		} else {
			normalized = append(normalized, evidence.NormalizeLedgerPath(p))
		}
	}
	return evidence.ExternalCustodyURLs(ledger, normalized), nil
}
