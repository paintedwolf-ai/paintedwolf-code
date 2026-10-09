package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"time"
)

func (s *Store) Record(ctx context.Context, in RecordInput) error {
	return s.RecordBatch(ctx, []RecordInput{in})
}

// RecordBatch persists one causal operation with ordered effects.
func (s *Store) RecordBatch(ctx context.Context, inputs []RecordInput) error {
	if s == nil || s.sqlDB == nil || len(inputs) == 0 {
		return nil
	}
	if err := validateBatch(inputs); err != nil {
		return err
	}
	s.recordMu.Lock()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		s.recordMu.Unlock()
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.recordBatchTx(ctx, s.queries.WithTx(tx), inputs); err != nil {
		s.recordMu.Unlock()
		return err
	}
	if err := tx.Commit(); err != nil {
		s.recordMu.Unlock()
		return err
	}
	s.recordMu.Unlock()
	// Leased or slow-to-reclaim blobs remain queued for a later pass.
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	_, _ = s.Retention.reclaimBlobBatch(bg)
	cancel()
	return nil
}

// RecordTx joins a caller transaction for a single-effect operation.
func (s *Store) RecordTx(ctx context.Context, tx *sql.Tx, in RecordInput) error {
	return s.RecordBatchTx(ctx, tx, []RecordInput{in})
}

// RecordFileTx records one effect and answers the file identity it landed on,
// which is new when the effect creates a path the ledger holds no file for.
func (s *Store) RecordFileTx(ctx context.Context, tx *sql.Tx, in RecordInput) (TrackedFile, error) {
	if s == nil || s.sqlDB == nil || tx == nil {
		return TrackedFile{}, fmt.Errorf("ledger not configured")
	}
	inputs := []RecordInput{in}
	if err := validateBatch(inputs); err != nil {
		return TrackedFile{}, err
	}
	return s.recordBatchIdentityTx(ctx, s.queries.WithTx(tx), inputs)
}

// RecordBatchTx joins a caller transaction for a multi-effect operation.
func (s *Store) RecordBatchTx(ctx context.Context, tx *sql.Tx, inputs []RecordInput) error {
	if s == nil || s.sqlDB == nil || tx == nil || len(inputs) == 0 {
		return nil
	}
	if err := validateBatch(inputs); err != nil {
		return err
	}
	return s.recordBatchTx(ctx, s.queries.WithTx(tx), inputs)
}

func validateBatch(inputs []RecordInput) error {
	first := inputs[0]
	if strings.TrimSpace(first.ProjectID) == "" || strings.TrimSpace(first.RootID) == "" ||
		strings.TrimSpace(first.Path) == "" || first.Op == "" || first.Origin == "" {
		return fmt.Errorf("source history requires project, root, path, operation, and origin")
	}
	for _, in := range inputs[1:] {
		if in.ProjectID != first.ProjectID || in.BranchID != first.BranchID ||
			in.Origin != first.Origin ||
			in.OperationID != first.OperationID || in.GitTransitionID != first.GitTransitionID ||
			in.CommandWindowID != first.CommandWindowID {
			return fmt.Errorf("source operation effects disagree on operation metadata")
		}
	}
	return nil
}

func (s *Store) recordBatchTx(ctx context.Context, q *db.Queries, inputs []RecordInput) error {
	_, err := s.recordBatchIdentityTx(ctx, q, inputs)
	return err
}

// recordBatchIdentityTx records the batch and answers the last effect's file
// identity. A replayed operation key answers an empty identity.
func (s *Store) recordBatchIdentityTx(ctx context.Context, q *db.Queries, inputs []RecordInput) (TrackedFile, error) {
	var recorded TrackedFile
	first := normalizedInput(inputs[0])
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
	for _, raw := range inputs {
		in := normalizedInput(raw)
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

func normalizedInput(in RecordInput) RecordInput {
	if in.TS.IsZero() {
		in.TS = time.Now().UTC()
	}
	if in.Cause == "" {
		switch in.Origin {
		case api.SourceChangeOriginAgent:
			in.Cause = "agent_tool"
		case api.SourceChangeOriginUser:
			in.Cause = "human_edit"
		default:
			in.Cause = "external_observation"
		}
	}
	if in.CaptureQuality == "" {
		in.CaptureQuality = "exact"
		if in.Origin == api.SourceChangeOriginExternal {
			in.CaptureQuality = "observed"
		}
	}
	if in.EntryKind == "" {
		in.EntryKind = EntryKindFile
	}
	if in.BeforeSHA256 == "" && in.Before != nil {
		in.BeforeSHA256 = sourceblob.ContentSHA(in.Before)
	}
	if in.AfterSHA256 == "" && in.After != nil {
		in.AfterSHA256 = sourceblob.ContentSHA(in.After)
	}
	if in.BeforeSize == 0 && in.Before != nil {
		in.BeforeSize = int64(len(in.Before))
	}
	if in.AfterSize == 0 && in.After != nil {
		in.AfterSize = int64(len(in.After))
	}
	return in
}

// recordEffect lands one effect and reports the file and version it produced.
func (s *Store) recordEffect(ctx context.Context, q *db.Queries, operationID string, in RecordInput, recorded *TrackedFile) error {
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

	// Preserve the observed pre-image when it differs from the tracked head.
	preSHA, preBytes, preSize, preRoot, prePath := in.BeforeSHA256, in.Before, in.BeforeSize, in.RootID, in.Path
	if in.Op == api.SourceChangeOpRename {
		preRoot, prePath = in.RootID, in.FromPath
		if preSHA == "" {
			preSHA, preBytes, preSize = in.AfterSHA256, in.After, in.AfterSize
		}
	}
	needsPreimage := in.Op != api.SourceChangeOpCreate && beforeVersionID == ""
	if headErr == nil && preSHA != "" && head.ContentSha256 != preSHA {
		needsPreimage = true
	}
	if needsPreimage {
		versionID, err := s.insertVersion(ctx, q, versionSpec{
			FileID: fileID, ProjectID: in.ProjectID, BranchID: in.BranchID,
			ParentVersionID:      beforeVersionID,
			DerivedFromVersionID: in.DerivedFromVersionID,
			RootID:               preRoot, Path: prePath, EntryKind: in.EntryKind,
			SHA256: preSHA, Content: preBytes, Size: preSize,
			CaptureQuality: in.CaptureQuality, TS: in.TS,
		})
		if err != nil {
			return err
		}
		beforeVersionID = versionID
	}

	parentVersionID := beforeVersionID
	state, afterSHA, afterBytes, afterSize := "content", in.AfterSHA256, in.After, in.AfterSize
	switch {
	case in.Op == api.SourceChangeOpDelete:
		state, afterSHA, afterBytes, afterSize = "absent", "", nil, 0
	case in.EntryKind == EntryKindDirectory:
		state, afterSHA, afterBytes, afterSize = "directory", "", nil, 0
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
		SHA256: afterSHA, Content: afterBytes, Size: afterSize,
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
	if err := q.UpsertSourceBranchHead(ctx, db.UpsertSourceBranchHeadParams{
		ProjectID: in.ProjectID, BranchID: in.BranchID.String(),
		FileID: fileID, VersionID: afterVersionID,
		RootID: in.RootID, Path: in.Path, State: state, ContentSha256: afterSHA,
		Ordinal: ordinal, ObservedTs: in.TS.Format(time.RFC3339Nano),
	}); err != nil {
		return err
	}
	hasAgent, err := recordPublicationAuthorship(ctx, q, beforeVersionID, afterVersionID, effectID, in)
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
	if err := updateLineAttribution(ctx, q, in, fileID, effectID); err != nil {
		return err
	}
	if in.EntryKind == EntryKindDirectory &&
		(in.Op == api.SourceChangeOpRename || in.Op == api.SourceChangeOpDelete) {
		return s.followDirectoryTransition(ctx, q, operationID, in)
	}
	return nil
}

// followDirectoryTransition advances tracked descendants.
func (s *Store) followDirectoryTransition(
	ctx context.Context,
	q *db.Queries,
	operationID string,
	in RecordInput,
) error {
	fromPath := in.Path
	if in.Op == api.SourceChangeOpRename {
		fromPath = in.FromPath
	}
	heads, err := q.ListSourceBranchHeadsUnderPath(ctx, db.ListSourceBranchHeadsUnderPathParams{
		ProjectID: in.ProjectID, BranchID: in.BranchID.String(), RootID: in.RootID,
		ParentPath: fromPath,
	})
	if err != nil {
		return err
	}
	for _, head := range heads {
		parent, err := q.GetSourceVersion(ctx, head.VersionID)
		if err != nil {
			return err
		}
		file, err := q.GetSourceFile(ctx, head.FileID)
		if err != nil {
			return err
		}
		path, state, sha, size := head.Path, "absent", "", int64(0)
		if in.Op == api.SourceChangeOpRename {
			path = in.Path + strings.TrimPrefix(head.Path, fromPath)
			state, sha, size = head.State, head.ContentSha256, parent.ByteSize
		}
		versionID, err := s.insertVersion(ctx, q, versionSpec{
			FileID: head.FileID, ProjectID: in.ProjectID, BranchID: in.BranchID,
			ParentVersionID: head.VersionID,
			OperationID:     operationID, RootID: in.RootID, Path: path,
			EntryKind: file.EntryKind, State: state,
			SHA256: sha, Size: size, CaptureQuality: in.CaptureQuality, TS: in.TS,
		})
		if err != nil {
			return err
		}
		ordinal, err := q.AdvanceSourceOrdinal(ctx, in.ProjectID)
		if err != nil {
			return err
		}
		effectID := newID()
		fromRootID, priorPath := "", ""
		if in.Op == api.SourceChangeOpRename {
			fromRootID, priorPath = in.RootID, head.Path
		}
		if err := q.InsertSourceEffect(ctx, db.InsertSourceEffectParams{
			ID: effectID, ProjectID: in.ProjectID, OperationID: operationID,
			FileID: head.FileID, BeforeVersionID: nullableString(head.VersionID),
			AfterVersionID: versionID, RootID: in.RootID, Path: path,
			FromRootID: fromRootID, FromPath: priorPath, Op: string(in.Op),
			EntryKind: file.EntryKind, Ordinal: ordinal,
			WalkVisible: 0, CreatedTs: in.TS.Format(time.RFC3339Nano),
		}); err != nil {
			return err
		}
		if err := q.UpsertSourceBranchHead(ctx, db.UpsertSourceBranchHeadParams{
			ProjectID: in.ProjectID, BranchID: in.BranchID.String(),
			FileID:    head.FileID,
			VersionID: versionID, RootID: in.RootID, Path: path, State: state,
			ContentSha256: sha, Ordinal: ordinal,
			ObservedTs: in.TS.Format(time.RFC3339Nano),
		}); err != nil {
			return err
		}
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
