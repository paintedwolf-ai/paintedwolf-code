package store

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/db"
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

// Preserve custody order across the records from one program.
func programProvenanceRecords(roots evidence.CitationRoots, prov evidence.ProgramHTTPProvenance) []evidence.Record {
	var recs []evidence.Record
	for _, rec := range [programCustodyRecordSlots]evidence.Record{
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
	intent := programPublicationIntent{Target: target, Provenance: prov}
	unlock := intent.lock(ev.core)
	defer unlock()
	release := bloblifecycle.AcquirePublication(ev.core.dataDir)
	defer release()
	tx, err := ev.core.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	records, err := ev.writeProgramProvenanceTargets(ctx, ev.core.queries.WithTx(tx), intent, programCustodyReservation{})
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	records.applyTrust(ev.core, target)
	if len(records.primary) == 0 {
		return "", nil
	}
	return records.primary[0].Handle, nil
}

type programProvenanceWrites struct {
	primary, shared []evidence.Record
}

func (w programProvenanceWrites) applyTrust(s *sqlCore, target evidence.ProgramProvenanceTarget) {
	for _, rec := range w.primary {
		bumpUntrusted(s, target.SessionID, rec)
		bumpSecretExposure(s, target.SessionID, rec)
	}
	for _, rec := range w.shared {
		bumpUntrusted(s, target.ChatSessionID, rec)
		bumpSecretExposure(s, target.ChatSessionID, rec)
	}
}

func (ev *sqlEvidence) writeProgramProvenanceTargets(ctx context.Context, q *db.Queries, intent programPublicationIntent, reservation programCustodyReservation) (programProvenanceWrites, error) {
	target, prov := intent.Target, intent.Provenance
	var writes programProvenanceWrites
	projectID, err := q.GetSessionProjectID(ctx, target.SessionID)
	if err != nil {
		return writes, err
	}
	writes.primary, err = ev.writeProgramProvenance(ctx, q, target.SessionID, projectID, target.ProjectDir, prov, reservation.primary)
	if err != nil || !intent.sharesCustody() {
		return writes, err
	}
	chatProjectID, err := q.GetSessionProjectID(ctx, target.ChatSessionID)
	if err != nil {
		return writes, err
	}
	if chatProjectID != projectID {
		return writes, fmt.Errorf("program provenance spans different projects")
	}
	prov.UntrustedContent = false
	writes.shared, err = ev.writeProgramProvenance(ctx, q, target.ChatSessionID, chatProjectID, target.ProjectDir, prov, reservation.shared)
	return writes, err
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

func (ev *sqlEvidence) writeProgramProvenance(ctx context.Context, q *db.Queries, sessionID, projectID, projectDir string, prov evidence.ProgramHTTPProvenance, nextSeq int) ([]evidence.Record, error) {
	roots, err := sessionCitationRoots(ctx, q, sessionID, projectDir)
	if err != nil {
		return nil, err
	}
	recs := programProvenanceRecords(roots, prov)
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
