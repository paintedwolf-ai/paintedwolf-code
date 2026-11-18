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
	if err := projectUntrustedLedger(ctx, tx, projectID, sessionID, rec); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	bumpUntrusted(s, sessionID, rec)
	bumpSecretExposure(s, sessionID, rec)
	return rec.Handle, nil
}

// programProvenanceRecords are the records one command program's network
// reads produce, in custody order: what it received, the directories it
// filled, and the external-custody paths it replaced locally.
func programProvenanceRecords(roots evidence.CitationRoots, prov evidence.ProgramHTTPProvenance) []evidence.Record {
	var recs []evidence.Record
	for _, rec := range []evidence.Record{
		evidence.BuildProgramHTTPRecord(roots, prov),
		evidence.BuildProgramTreeRecord(roots, prov),
		evidence.BuildProgramCustodyReplacementRecord(roots, prov),
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
	if len(programProvenanceRecords(evidence.CitationRoots{ProjectDir: projectDir}, prov)) == 0 {
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
	roots, err := sessionCitationRoots(ctx, q, sessionID, projectDir)
	if err != nil {
		return "", err
	}
	recs := programProvenanceRecords(roots, prov)
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
	for _, rec := range inserted {
		if err := projectUntrustedLedger(ctx, tx, projectID, sessionID, rec); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	for _, rec := range inserted {
		bumpUntrusted(s, sessionID, rec)
		bumpSecretExposure(s, sessionID, rec)
	}
	return recs[0].Handle, nil
}

// ExternalCustodyURLsForPaths returns external URLs associated with any of paths in sessionID.
func (ev *sqlEvidence) ExternalCustodyURLsForPaths(ctx context.Context, sessionID, projectDir string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	roots, err := sessionCitationRoots(ctx, ev.core.queries, sessionID, projectDir)
	if err != nil {
		return nil, err
	}
	metaLedger, err := loadMetadataLedger(ctx, ev.core.queries, sessionID, roots)
	if err != nil {
		return nil, err
	}
	return evidence.ExternalCustodyURLs(metaLedger, custodyQueryKeys(roots, paths)), nil
}

// custodyQueryKeys maps path tokens to the keys custody is indexed under, so
// each spelling of a path finds its own folder's custody and no other's.
func custodyQueryKeys(roots evidence.CitationRoots, paths []string) []string {
	keys := make([]string, 0, len(paths))
	for _, p := range paths {
		keys = append(keys, evidence.CustodyKey(roots, p))
	}
	return keys
}
