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

// CommitProgramHTTPProvenance persists an http_response observation for a command program
// that delivered external HTTP content or wrote external custody files, and records
// whole-file custody replacement for any cleared paths.
func (ev *sqlEvidence) CommitProgramHTTPProvenance(ctx context.Context, sessionID, projectDir string, prov evidence.ProgramHTTPProvenance) (string, error) {
	httpRec := evidence.BuildProgramHTTPRecord(projectDir, prov)
	clearRec := evidence.BuildProgramCustodyReplacementRecord(projectDir, prov)
	if httpRec.Kind == "" && clearRec.Kind == "" {
		return "", nil
	}
	s := ev.core
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
	var retHandle string
	var httpInserted bool
	if httpRec.Kind != "" {
		ordinal, err := nextEvidenceOrdinal(ctx, q, sessionID, httpRec.Kind)
		if err != nil {
			return "", err
		}
		httpRec.Handle = evidence.FormatHandle(httpRec.Kind, ordinal)
		httpInserted, err = writeEvidenceRecord(ctx, q, s.dataDir, sessionID, projectID, httpRec)
		if err != nil {
			return "", err
		}
		retHandle = httpRec.Handle
	}
	if clearRec.Kind != "" {
		ordinal, err := nextEvidenceOrdinal(ctx, q, sessionID, clearRec.Kind)
		if err != nil {
			return "", err
		}
		clearRec.Handle = evidence.FormatHandle(clearRec.Kind, ordinal)
		if _, err := writeEvidenceRecord(ctx, q, s.dataDir, sessionID, projectID, clearRec); err != nil {
			return "", err
		}
		if retHandle == "" {
			retHandle = clearRec.Handle
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if httpInserted {
		bumpUntrusted(s, sessionID, httpRec)
		bumpSecretExposure(s, sessionID, httpRec)
		if err := s.projectUntrustedLedger(ctx, nil, sessionID, httpRec); err != nil {
			return "", err
		}
	}
	return retHandle, nil
}

func (ev *memoryEvidence) CommitProgramHTTPProvenance(ctx context.Context, sessionID, projectDir string, prov evidence.ProgramHTTPProvenance) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	httpRec := evidence.BuildProgramHTTPRecord(projectDir, prov)
	clearRec := evidence.BuildProgramCustodyReplacementRecord(projectDir, prov)
	if httpRec.Kind == "" && clearRec.Kind == "" {
		return "", nil
	}
	s := ev.core
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return "", ErrSessionNotFound
	}
	var retHandle string
	if httpRec.Kind != "" {
		httpRec.Handle = evidence.FormatHandle(httpRec.Kind, nextEvidenceOrdinalLocked(s, sessionID, httpRec.Kind))
		putMemoryEvidenceRowLocked(s, sessionID, httpRec.Handle, httpRec)
		bumpUntrustedLocked(s, sessionID, httpRec)
		bumpSecretExposureLocked(s, sessionID, httpRec)
		retHandle = httpRec.Handle
	}
	if clearRec.Kind != "" {
		clearRec.Handle = evidence.FormatHandle(clearRec.Kind, nextEvidenceOrdinalLocked(s, sessionID, clearRec.Kind))
		putMemoryEvidenceRowLocked(s, sessionID, clearRec.Handle, clearRec)
		if retHandle == "" {
			retHandle = clearRec.Handle
		}
	}
	return retHandle, nil
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

