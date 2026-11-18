package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
)

const (
	maxProgramPublicationIntentBytes = 1 << 20
	maxProgramPublicationPage        = 256
	programCustodyRecordSlots        = 3
)

type programPublicationIntent struct {
	Target     evidence.ProgramProvenanceTarget `json:"target"`
	Provenance evidence.ProgramHTTPProvenance   `json:"provenance"`
}

func (i programPublicationIntent) affectsCustody() bool {
	return len(i.Provenance.CustodyPaths)+len(i.Provenance.CustodyTrees)+len(i.Provenance.ClearedPaths) > 0
}

func (i programPublicationIntent) sharesCustody() bool {
	return i.Target.ChatSessionID != "" && i.Target.ChatSessionID != i.Target.SessionID && i.affectsCustody()
}

func (i programPublicationIntent) lock(s *sqlCore) func() {
	return s.lockProvenanceTarget(i.Target.SessionID, i.Target.ChatSessionID, i.sharesCustody())
}

func (i programPublicationIntent) encode() (string, error) {
	p := i.Provenance
	remaining := maxProgramPublicationIntentBytes
	for _, group := range [][]string{
		{i.Target.SessionID, i.Target.ChatSessionID, i.Target.ProjectDir, p.ProgramID},
		p.ObservedURLs, p.CustodyPaths, p.CustodyTrees, p.ClearedPaths,
	} {
		for _, value := range group {
			if !utf8.ValidString(value) {
				return "", fmt.Errorf("program publication intent is not valid UTF-8")
			}
			remaining -= len(value) + 1
			if remaining < 0 {
				return "", fmt.Errorf("program publication intent exceeds the storage limit")
			}
		}
	}
	data, err := json.Marshal(i)
	if err != nil {
		return "", err
	}
	if len(data) > maxProgramPublicationIntentBytes {
		return "", fmt.Errorf("program publication intent exceeds the storage limit")
	}
	return string(data), nil
}

func programPublicationIntentDigest(data string) string {
	digest := sha256.Sum256([]byte(data))
	return hex.EncodeToString(digest[:])
}

func (ev *sqlEvidence) BeginProgramPublication(ctx context.Context, sessionID, programID string) error {
	q := ev.core.queries
	count, err := q.BeginProgramPublication(ctx, db.BeginProgramPublicationParams{SessionID: sessionID, ProgramID: programID})
	if err != nil || count == 1 {
		return err
	}
	_, err = q.GetProgramPublication(ctx, db.GetProgramPublicationParams{SessionID: sessionID, ProgramID: programID})
	return err
}

func (ev *sqlEvidence) StageProgramPublication(ctx context.Context, target evidence.ProgramProvenanceTarget, prov evidence.ProgramHTTPProvenance) error {
	intent := programPublicationIntent{Target: target, Provenance: prov}
	data, err := intent.encode()
	if err != nil {
		return err
	}
	digest := programPublicationIntentDigest(data)
	s := ev.core
	unlock := intent.lock(s)
	defer unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	key := db.GetProgramPublicationParams{SessionID: target.SessionID, ProgramID: prov.ProgramID}
	row, err := q.GetProgramPublication(ctx, key)
	if err != nil {
		return err
	}
	if row.State != "preparing" {
		if row.IntentDigest != digest {
			return fmt.Errorf("program publication already has a different intent")
		}
		return nil
	}

	reservation, err := intent.reserveCustody(ctx, q)
	if err != nil {
		return err
	}
	count, err := q.StageProgramPublication(ctx, db.StageProgramPublicationParams{
		SessionID: target.SessionID, ProgramID: prov.ProgramID, IntentJson: data, IntentDigest: digest,
		PrimaryCustodyStart: int64(reservation.primary), ChatSessionID: reservation.chatSessionID,
		SharedCustodyStart: int64(reservation.shared),
	})
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("program publication changed before staging")
	}
	return tx.Commit()
}

type programCustodyReservation struct {
	primary, shared int
	chatSessionID   string
}

func (i programPublicationIntent) reserveCustody(ctx context.Context, q *db.Queries) (programCustodyReservation, error) {
	var reservation programCustodyReservation
	if !i.affectsCustody() {
		return reservation, nil
	}
	primary, err := nextCustodySeq(ctx, q, i.Target.SessionID)
	if err != nil {
		return reservation, err
	}
	reservation.primary = primary
	if i.sharesCustody() {
		shared, err := nextCustodySeq(ctx, q, i.Target.ChatSessionID)
		if err != nil {
			return reservation, err
		}
		reservation.chatSessionID = i.Target.ChatSessionID
		reservation.shared = shared
	}
	return reservation, nil
}

func (ev *sqlEvidence) CommitProgramPublication(ctx context.Context, sessionID, programID string) error {
	s := ev.core
	key := db.GetProgramPublicationParams{SessionID: sessionID, ProgramID: programID}
	row, err := s.queries.GetProgramPublication(ctx, key)
	if err != nil || row.State == "committed" {
		return err
	}
	if row.State != "staged" {
		return fmt.Errorf("program publication has no staged intent")
	}
	if programPublicationIntentDigest(row.IntentJson) != row.IntentDigest {
		return fmt.Errorf("program publication intent digest mismatch")
	}
	var intent programPublicationIntent
	if err := json.Unmarshal([]byte(row.IntentJson), &intent); err != nil {
		return err
	}
	if intent.Target.SessionID != sessionID || intent.Provenance.ProgramID != programID {
		return fmt.Errorf("program publication intent belongs to another program")
	}
	unlock := intent.lock(s)
	defer unlock()
	release := bloblifecycle.AcquirePublication(s.dataDir)
	defer release()
	reservation := programCustodyReservation{
		primary: int(row.PrimaryCustodyStart), shared: int(row.SharedCustodyStart),
	}
	if (reservation.primary > 0) != intent.affectsCustody() || (reservation.shared > 0) != intent.sharesCustody() {
		return fmt.Errorf("program publication custody reservation differs from intent")
	}
	return ev.commitStagedProgramPublication(ctx, intent, row.IntentDigest, reservation)
}

func (ev *sqlEvidence) commitStagedProgramPublication(ctx context.Context, intent programPublicationIntent, digest string, reservation programCustodyReservation) error {
	s := ev.core
	target := intent.Target
	programID := intent.Provenance.ProgramID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	count, err := q.CommitProgramPublication(ctx, db.CommitProgramPublicationParams{
		SessionID: target.SessionID, ProgramID: programID, IntentDigest: digest,
	})
	if err != nil {
		return err
	}
	if count == 0 {
		// Another publisher can commit between reading the intent and taking the writer.
		row, err := q.GetProgramPublication(ctx, db.GetProgramPublicationParams{SessionID: target.SessionID, ProgramID: programID})
		if err != nil || (row.State == "committed" && row.IntentDigest == digest) {
			return err
		}
		return fmt.Errorf("staged program publication changed before commit")
	}
	records, err := ev.writeProgramProvenanceTargets(ctx, q, intent, reservation)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	records.applyTrust(s, target)
	return nil
}

func (ev *sqlEvidence) MarkProgramPublicationMaterialized(ctx context.Context, sessionID, programID string) error {
	count, err := ev.core.queries.MarkProgramPublicationMaterialized(ctx, db.MarkProgramPublicationMaterializedParams{
		SessionID: sessionID, ProgramID: programID,
	})
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("program publication must be committed before materialization")
	}
	return nil
}

func (ev *sqlEvidence) ListRecoverableProgramPublications(ctx context.Context, afterProgramID string, limit int) ([]evidence.ProgramPublicationKey, error) {
	if limit < 1 || limit > maxProgramPublicationPage {
		return nil, fmt.Errorf("invalid program publication page size")
	}
	rows, err := ev.core.queries.ListRecoverableProgramPublications(ctx, db.ListRecoverableProgramPublicationsParams{
		AfterProgramID: afterProgramID, PageSize: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	keys := make([]evidence.ProgramPublicationKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, evidence.ProgramPublicationKey{SessionID: row.SessionID, ProgramID: row.ProgramID})
	}
	return keys, nil
}
