package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/pkg/api"
)

func (s *Store) Record(ctx context.Context, in RecordInput) error {
	return s.RecordBatch(ctx, []RecordInput{in})
}

// RecordBatch persists one causal operation with ordered effects.
func (s *Store) RecordBatch(ctx context.Context, inputs []RecordInput) error {
	if s == nil || s.sqlDB == nil || len(inputs) == 0 {
		return nil
	}
	prepared, err := s.Prepare(ctx, inputs)
	if err != nil {
		return err
	}
	defer prepared.Close()
	s.recordMu.Lock()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		s.recordMu.Unlock()
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := prepared.CommitTx(ctx, tx); err != nil {
		s.recordMu.Unlock()
		return err
	}
	if err := tx.Commit(); err != nil {
		s.recordMu.Unlock()
		return err
	}
	s.recordMu.Unlock()
	prepared.Close()
	// Leased or slow-to-reclaim blobs remain queued for a later pass.
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	_, _ = s.Retention.reclaimBlobBatch(bg)
	cancel()
	return nil
}

func (s *Store) recordBatchTx(ctx context.Context, q *db.Queries, inputs []preparedRecord) error {
	_, err := s.recordBatchIdentityTx(ctx, q, inputs)
	return err
}

// recordBatchIdentityTx records the batch and answers the last effect's file
// identity. A replayed operation key answers an empty identity.
func (s *Store) recordBatchIdentityTx(ctx context.Context, q *db.Queries, inputs []preparedRecord) (TrackedFile, error) {
	var recorded TrackedFile
	first := inputs[0].RecordInput
	if first.OperationID != "" {
		_, err := q.GetSourceOperationByKey(ctx, db.GetSourceOperationByKeyParams{
			ProjectID: first.ProjectID, OperationKey: first.OperationID,
		})
		if err == nil {
			return recorded, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return recorded, err
		}
	}
	personID, err := s.operationPerson(ctx, first)
	if err != nil {
		return recorded, err
	}
	operationID := newID()
	ts := first.TS.Format(time.RFC3339Nano)
	if err := q.InsertSourceOperation(ctx, db.InsertSourceOperationParams{
		ID: operationID, ProjectID: first.ProjectID, BranchID: first.BranchID.String(),
		Origin: string(first.Origin), PersonID: db.NullString(personID),
		Cause: first.Cause, ActorLabel: first.ActorLabel, SessionID: first.SessionID,
		JobID: first.JobID, Turn: int64(first.Turn), ToolCallID: first.ToolCallID,
		ToolName: first.ToolName,
		BatchID:  first.BatchID, OperationKey: first.OperationID,
		CaptureQuality: first.CaptureQuality, StartedTs: ts, CommittedTs: ts,
		GitTransitionID: nullableString(first.GitTransitionID),
		CommandWindowID: nullableString(first.CommandWindowID),
	}); err != nil {
		return recorded, err
	}
	for _, in := range inputs {
		if err := s.recordEffect(ctx, q, operationID, in, &recorded); err != nil {
			return recorded, err
		}
	}
	return recorded, nil
}

// operationPerson names the person behind a user operation; other origins have none.
func (s *Store) operationPerson(ctx context.Context, in RecordInput) (string, error) {
	if in.Origin != api.SourceChangeOriginUser {
		return "", nil
	}
	if in.PersonID != "" {
		return in.PersonID, nil
	}
	person, err := people.Acting(ctx, s.people)
	if err != nil {
		return "", fmt.Errorf("source operation person: %w", err)
	}
	return person.ID, nil
}

// recordEffect lands one effect and reports the file and version it produced.
func (s *Store) recordEffect(ctx context.Context, q *db.Queries, operationID string, in preparedRecord, recorded *TrackedFile) error {
	if recovery := in.NativeRecovery; recovery != nil && recovery.Restore && recovery.Key != "" {
		fileID, err := q.GetSourceRecoveryFile(ctx, db.GetSourceRecoveryFileParams{ProjectID: in.ProjectID, BranchID: in.BranchID.String(), RootID: in.RootID, OperationKey: recovery.Key})
		if err != nil {
			return err
		}
		if in.FileID != "" && in.FileID != fileID {
			return fmt.Errorf("native recovery refers to another file")
		}
		in.FileID = fileID
	}
	lookupPath := in.Path
	if in.Op == api.SourceChangeOpRename && in.FromPath != "" {
		lookupPath = in.FromPath
	}
	var head db.SourceBranchHeads
	var headErr error
	if in.FileID != "" {
		head, headErr = q.GetSourceBranchHeadByFile(ctx, db.GetSourceBranchHeadByFileParams{
			ProjectID: in.ProjectID, BranchID: in.BranchID.String(), FileID: in.FileID,
		})
	} else {
		head, headErr = q.GetSourceBranchHeadByPath(ctx, db.GetSourceBranchHeadByPathParams{
			ProjectID: in.ProjectID, BranchID: in.BranchID.String(), RootID: in.RootID, Path: lookupPath,
		})
	}
	if headErr != nil && !errors.Is(headErr, sql.ErrNoRows) {
		return headErr
	}
	// A published creation is a new identity unless recovery names the original.
	if in.Op == api.SourceChangeOpCreate && in.FileID == "" {
		headErr = sql.ErrNoRows
	}
	if errors.Is(headErr, sql.ErrNoRows) && in.FileID == "" && in.BranchID.IsWorker() {
		trunk, err := q.GetTrunkSourceHeadByPath(ctx, db.GetTrunkSourceHeadByPathParams{
			ProjectID: in.ProjectID, RootID: in.RootID, Path: lookupPath,
		})
		if err == nil {
			in.FileID = trunk.FileID
			if in.DerivedFromVersionID == "" {
				in.DerivedFromVersionID = trunk.VersionID
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}

	fileID := in.FileID
	beforeVersionID := ""
	if headErr == nil {
		fileID, beforeVersionID = head.FileID, head.VersionID
	} else {
		if fileID == "" {
			fileID = newID()
			if err := q.InsertSourceFile(ctx, db.InsertSourceFileParams{
				ID: fileID, ProjectID: in.ProjectID, EntryKind: in.EntryKind,
				CreatedTs: in.TS.Format(time.RFC3339Nano),
			}); err != nil {
				return err
			}
		} else {
			file, err := q.GetSourceFile(ctx, fileID)
			if err != nil {
				return err
			}
			if file.ProjectID != in.ProjectID {
				return fmt.Errorf("source file belongs to another project")
			}
		}
	}

	var priorHead *db.SourceBranchHeads
	if headErr == nil {
		priorHead = &head
	}
	beforeVersionID, err := s.preserveObservedPreimage(ctx, q, in, TrackedFile{FileID: fileID, VersionID: beforeVersionID}, priorHead)
	if err != nil {
		return err
	}

	parentVersionID := beforeVersionID
	state, afterSHA, afterSize := "content", in.AfterSHA256, in.AfterSize
	switch {
	case in.Op == api.SourceChangeOpDelete:
		state, afterSHA, afterSize = "absent", "", 0
	case in.EntryKind == EntryKindDirectory:
		state, afterSHA, afterSize = "directory", "", 0
	case in.Op == api.SourceChangeOpRename && afterSHA == "" && headErr == nil:
		afterSHA = head.ContentSha256
		parent, err := q.GetSourceVersion(ctx, head.VersionID)
		if err != nil {
			return err
		}
		afterSize = parent.ByteSize
	}
	afterVersionID, err := s.insertVersion(ctx, q, versionSpec{
		FileID: fileID, ProjectID: in.ProjectID, BranchID: in.BranchID,
		ParentVersionID:      parentVersionID,
		DerivedFromVersionID: in.DerivedFromVersionID, OperationID: operationID,
		RootID: in.RootID, Path: in.Path, EntryKind: in.EntryKind, State: state,
		SHA256: afterSHA, Object: in.objects[afterSHA], Size: afterSize,
		CaptureQuality: in.CaptureQuality, TS: in.TS,
	})
	if err != nil {
		return err
	}
	if recorded != nil {
		*recorded = TrackedFile{FileID: fileID, VersionID: afterVersionID}
	}
	ordinal, err := q.AdvanceSourceOrdinal(ctx, in.ProjectID)
	if err != nil {
		return err
	}
	effectID := newID()
	fromRootID := in.FromRootID
	if fromRootID == "" && in.FromPath != "" {
		fromRootID = in.RootID
	}
	if err := q.InsertSourceEffect(ctx, db.InsertSourceEffectParams{
		ID: effectID, ProjectID: in.ProjectID, OperationID: operationID,
		FileID: fileID, BeforeVersionID: nullableString(beforeVersionID), AfterVersionID: afterVersionID,
		RootID: in.RootID, Path: in.Path, FromRootID: fromRootID, FromPath: in.FromPath,
		Op: string(in.Op), EntryKind: in.EntryKind, Ordinal: ordinal,
		WalkVisible: 1, CreatedTs: in.TS.Format(time.RFC3339Nano),
	}); err != nil {
		return err
	}
	if err := transitionDirectory(ctx, q, in.RecordInput, ordinal); err != nil {
		return err
	}
	if err := upsertSourceHead(ctx, q, db.SourceBranchHeads{
		ProjectID: in.ProjectID, BranchID: in.BranchID.String(),
		FileID: fileID, VersionID: afterVersionID,
		RootID: in.RootID, Path: in.Path, State: state, ContentSha256: afterSHA,
		Ordinal: ordinal, ObservedTs: in.TS.Format(time.RFC3339Nano),
	}); err != nil {
		return err
	}
	hasAgent, err := recordPublicationAuthorship(ctx, q, beforeVersionID, afterVersionID, effectID, in.RecordInput)
	if err != nil {
		return err
	}
	if hasAgent && !in.BranchID.IsWorker() {
		if err := q.InsertSourceAgentPresentation(ctx, db.InsertSourceAgentPresentationParams{
			EffectID: effectID, ProjectID: in.ProjectID, FileID: fileID, Ordinal: ordinal,
		}); err != nil {
			return err
		}
	}
	if err := updateLineAttribution(ctx, q, in.RecordInput, fileID, effectID); err != nil {
		return err
	}

	return nil
}

func updateLineAttribution(
	ctx context.Context,
	q *db.Queries,
	in RecordInput,
	fileID, effectID string,
) error {
	if in.Origin == api.SourceChangeOriginExternal || in.Op == api.SourceChangeOpDelete {
		return q.DeleteSourceLineAttrForFile(ctx, db.DeleteSourceLineAttrForFileParams{
			ProjectID: in.ProjectID, BranchID: in.BranchID.String(), FileID: fileID,
		})
	}
	beforeText, beforeOK := "", true
	if in.Before != nil {
		beforeText, beforeOK = decodeTextSnapshot(in.Before)
	}
	afterText, afterOK := decodeTextSnapshot(in.After)
	existing, err := q.ListSourceLineAttrForFile(ctx, db.ListSourceLineAttrForFileParams{
		ProjectID: in.ProjectID, BranchID: in.BranchID.String(), FileID: fileID,
	})
	if err != nil {
		return err
	}
	intervals := make([]Interval, 0, len(existing))
	for _, row := range existing {
		intervals = append(intervals, Interval{
			Start: int(row.StartLine), End: int(row.EndLine), ChangeID: row.EffectID,
		})
	}
	var next []Interval
	if beforeOK && afterOK {
		next = ApplyAttribution(intervals, beforeText, afterText, effectID)
	}
	if err := q.DeleteSourceLineAttrForFile(ctx, db.DeleteSourceLineAttrForFileParams{
		ProjectID: in.ProjectID, BranchID: in.BranchID.String(), FileID: fileID,
	}); err != nil {
		return err
	}
	for _, interval := range next {
		if err := q.InsertSourceLineAttr(ctx, db.InsertSourceLineAttrParams{
			ProjectID: in.ProjectID, BranchID: in.BranchID.String(), FileID: fileID,
			StartLine: int64(interval.Start), EndLine: int64(interval.End), EffectID: interval.ChangeID,
		}); err != nil {
			return err
		}
	}
	return nil
}
