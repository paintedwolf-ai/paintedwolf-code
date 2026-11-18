package store

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/db"

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
func (ev *sqlEvidence) CommitProgramHTTPProvenance(ctx context.Context, target evidence.ProgramProvenanceTarget, prov evidence.ProgramHTTPProvenance) (string, error) {
	if len(programProvenanceRecords(evidence.CitationRoots{ProjectDir: target.ProjectDir}, prov)) == 0 {
		return "", nil
	}
	chat := target.ChatSessionID
	sharesCustody := chat != "" && chat != target.SessionID && (len(prov.CustodyPaths)+len(prov.CustodyTrees)+len(prov.ClearedPaths) > 0)
	s := ev.core
	unlock := s.lockProvenanceTarget(target.SessionID, chat, sharesCustody)
	defer unlock()
	release := bloblifecycle.AcquirePublication(s.dataDir)
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	projectID, err := q.GetSessionProjectID(ctx, target.SessionID)
	if err != nil {
		return "", err
	}
	primary, err := ev.writeProgramProvenance(ctx, q, target.SessionID, projectID, target.ProjectDir, prov)
	if err != nil {
		return "", err
	}
	var shared []evidence.Record
	if sharesCustody {
		chatProjectID, err := q.GetSessionProjectID(ctx, chat)
		if err != nil {
			return "", err
		}
		if chatProjectID != projectID {
			return "", fmt.Errorf("program provenance spans different projects")
		}
		prov.UntrustedContent = false
		shared, err = ev.writeProgramProvenance(ctx, q, chat, chatProjectID, target.ProjectDir, prov)
		if err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	for _, rec := range primary {
		bumpUntrusted(s, target.SessionID, rec)
		bumpSecretExposure(s, target.SessionID, rec)
	}
	for _, rec := range shared {
		bumpUntrusted(s, chat, rec)
		bumpSecretExposure(s, chat, rec)
	}
	if len(primary) == 0 {
		return "", nil
	}
	return primary[0].Handle, nil
}

func (s *sqlCore) lockProvenanceTarget(sessionID, chatID string, shared bool) func() {
	if !shared {
		return s.lockMutation(sessionID)
	}
	n := uint(len(s.mutationLocks))
	first, second := mutationShard(sessionID)%n, mutationShard(chatID)%n
	if first > second {
		first, second = second, first
	}
	s.mutationLocks[first].Lock()
	if first == second {
		return s.mutationLocks[first].Unlock
	}
	s.mutationLocks[second].Lock()
	return func() {
		s.mutationLocks[second].Unlock()
		s.mutationLocks[first].Unlock()
	}
}

func (ev *sqlEvidence) writeProgramProvenance(ctx context.Context, q *db.Queries, sessionID, projectID, projectDir string, prov evidence.ProgramHTTPProvenance) ([]evidence.Record, error) {
	roots, err := sessionCitationRoots(ctx, q, sessionID, projectDir)
	if err != nil {
		return nil, err
	}
	recs := programProvenanceRecords(roots, prov)
	nextSeq := 0
	var inserted []evidence.Record
	for i := range recs {
		rec := &recs[i]
		ordinal, err := nextEvidenceOrdinal(ctx, q, sessionID, rec.Kind)
		if err != nil {
			return nil, err
		}
		rec.Handle = evidence.FormatHandle(rec.Kind, ordinal)
		if evidence.ReplacesWholeFileCustody(*rec) && rec.CustodySeq() == 0 {
			if nextSeq == 0 {
				if nextSeq, err = nextCustodySeq(ctx, q, sessionID); err != nil {
					return nil, err
				}
			}
			rec.SetCustodySeq(nextSeq)
			nextSeq++
		}
		ok, err := writeEvidenceRecord(ctx, q, ev.core.dataDir, sessionID, projectID, *rec)
		if err != nil {
			return nil, err
		}
		if ok {
			inserted = append(inserted, *rec)
		}
	}
	return inserted, nil
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
