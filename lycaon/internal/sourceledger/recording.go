package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
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
	return s.recordBatchIdentityTxWithCache(ctx, q, inputs, nil)
}

func (s *Store) recordBatchIdentityTxWithCache(ctx context.Context, q *db.Queries, inputs []preparedRecord, cache *TxBatchCache) (TrackedFile, error) {
	if cache != nil && cache.tx != nil && cache.freshOperations && len(inputs) == 1 {
		if recorded, ok, err := s.bufferCreateEffect(ctx, q, inputs[0], cache); ok || err != nil {
			return recorded, err
		}
		if err := cache.flushBufferedInserts(ctx, cache.tx); err != nil {
			return TrackedFile{}, err
		}
	}
	var recorded TrackedFile
	first := inputs[0].RecordInput
	if first.OperationID != "" && (cache == nil || !cache.freshOperations) {
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
		if err := s.recordEffectWithCache(ctx, q, operationID, in, &recorded, cache); err != nil {
			return recorded, err
		}
	}
	return recorded, nil
}

func (s *Store) bufferCreateEffect(ctx context.Context, q *db.Queries, in preparedRecord, cache *TxBatchCache) (TrackedFile, bool, error) {
	if in.Op != api.SourceChangeOpCreate || in.FileID != "" || in.NativeRecovery != nil ||
		in.EntryKind != EntryKindFile || in.BranchID.IsWorker() ||
		in.Origin != api.SourceChangeOriginAgent || in.TextBefore != nil || in.TextAfter != nil ||
		in.AfterSHA256 == "" {
		return TrackedFile{}, false, nil
	}
	obj := in.objects[in.AfterSHA256]
	if obj == nil {
		return TrackedFile{}, false, nil
	}
	parent, err := directoryPathWithCache(ctx, q, in.ProjectID, in.BranchID.String(), in.RootID, path.Dir(in.Path), cache)
	if err != nil {
		return TrackedFile{}, false, err
	}
	if err := cache.ensureDirectoryEntriesLoaded(ctx, parent.ID); err != nil {
		return TrackedFile{}, false, err
	}
	name := path.Base(in.Path)
	entryKey := dirEntryKey{dirID: parent.ID, name: name}
	if cache.seenDirEntries[entryKey] {
		return TrackedFile{}, false, nil
	}
	if !cache.upsertedBlobs[obj.Sha256] {
		if err := q.UpsertSourceBlobObject(ctx, *obj); err != nil {
			return TrackedFile{}, false, err
		}
		cache.upsertedBlobs[obj.Sha256] = true
	}
	seq, err := cache.advanceOrdinal(ctx, q, in.ProjectID)
	if err != nil {
		return TrackedFile{}, false, err
	}
	ordinal, err := cache.advanceOrdinal(ctx, q, in.ProjectID)
	if err != nil {
		return TrackedFile{}, false, err
	}
	cache.seenDirEntries[entryKey] = true
	operationID := newID()
	fileID := newID()
	afterVersionID := newID()
	effectID := newID()
	ts := in.TS.Format(time.RFC3339Nano)
	branchStr := in.BranchID.String()

	cache.operations = append(cache.operations, db.InsertSourceOperationParams{
		ID: operationID, ProjectID: in.ProjectID, BranchID: branchStr,
		Origin: string(in.Origin), PersonID: db.NullString(""),
		Cause: in.Cause, ActorLabel: in.ActorLabel, SessionID: in.SessionID,
		JobID: in.JobID, Turn: int64(in.Turn), ToolCallID: in.ToolCallID,
		ToolName: in.ToolName, BatchID: in.BatchID, OperationKey: in.OperationID,
		CaptureQuality: in.CaptureQuality, StartedTs: ts, CommittedTs: ts,
		GitTransitionID: nullableString(in.GitTransitionID),
		CommandWindowID: nullableString(in.CommandWindowID),
	})
	cache.files = append(cache.files, db.InsertSourceFileParams{
		ID: fileID, ProjectID: in.ProjectID, EntryKind: in.EntryKind, CreatedTs: ts,
	})
	cache.versions = append(cache.versions, db.InsertSourceVersionParams{
		Seq: seq, ID: afterVersionID, FileID: fileID, ProjectID: in.ProjectID,
		BranchID: branchStr, DerivedFromVersionID: nullableString(in.DerivedFromVersionID),
		OperationID: nullableString(operationID), RootID: in.RootID, Path: in.Path,
		State: "content", ContentSha256: in.AfterSHA256, ByteSize: obj.Size,
		CaptureState: "stored", CaptureReason: "", CaptureQuality: in.CaptureQuality,
		CreatedTs: ts, Landing: LandingWorkingFile,
	})
	fromRootID := in.FromRootID
	if fromRootID == "" && in.FromPath != "" {
		fromRootID = in.RootID
	}
	cache.effects = append(cache.effects, db.InsertSourceEffectParams{
		ID: effectID, ProjectID: in.ProjectID, OperationID: operationID,
		FileID: fileID, AfterVersionID: afterVersionID,
		RootID: in.RootID, Path: in.Path, FromRootID: fromRootID, FromPath: in.FromPath,
		Op: string(in.Op), EntryKind: in.EntryKind, Ordinal: ordinal,
		WalkVisible: 1, CreatedTs: ts,
	})
	cache.heads = append(cache.heads, db.UpsertSourceHeadEntryParams{
		ProjectID: in.ProjectID, BranchID: branchStr, RootID: in.RootID,
		FileID: fileID, VersionID: afterVersionID, DirectoryID: parent.ID,
		Name: name, State: "content", ContentSha256: in.AfterSHA256,
		Ordinal: ordinal, ObservedTs: ts,
	})
	cache.presentations = append(cache.presentations, db.InsertSourceAgentPresentationParams{
		EffectID: effectID, ProjectID: in.ProjectID, FileID: fileID, Ordinal: ordinal,
	})
	if afterText, afterOK := decodeTextSnapshot(in.After); afterOK {
		for _, interval := range ApplyAttribution(nil, "", afterText, effectID) {
			cache.lineAttrs = append(cache.lineAttrs, db.InsertSourceLineAttrParams{
				ProjectID: in.ProjectID, BranchID: branchStr, FileID: fileID,
				StartLine: int64(interval.Start), EndLine: int64(interval.End), EffectID: interval.ChangeID,
			})
		}
	}
	return TrackedFile{FileID: fileID, VersionID: afterVersionID}, true, nil
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

// recordEffectWithCache lands one effect and reports the file and version it produced.
func (s *Store) recordEffectWithCache(ctx context.Context, q *db.Queries, operationID string, in preparedRecord, recorded *TrackedFile, cache *TxBatchCache) error {
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
	} else if in.Op == api.SourceChangeOpCreate {
		// A published creation has a new identity unless recovery named the original.
		headErr = sql.ErrNoRows
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
	isNewFile := false
	if headErr == nil {
		fileID, beforeVersionID = head.FileID, head.VersionID
	} else {
		if fileID == "" {
			fileID = newID()
			isNewFile = true
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
	beforeVersionID, err := s.preserveObservedPreimageWithCache(ctx, q, in, TrackedFile{FileID: fileID, VersionID: beforeVersionID}, priorHead, cache)
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

	if state == "content" && in.EntryKind == EntryKindLink {
		state = "link"
	}

	afterVersionID, err := s.insertVersionWithCache(ctx, q, versionSpec{

		FileID: fileID, ProjectID: in.ProjectID, BranchID: in.BranchID,
		ParentVersionID:      parentVersionID,
		DerivedFromVersionID: in.DerivedFromVersionID, OperationID: operationID,
		RootID: in.RootID, Path: in.Path, EntryKind: in.EntryKind, State: state,
		SHA256: afterSHA, Object: in.objects[afterSHA], Size: afterSize,
		CaptureQuality: in.CaptureQuality, TS: in.TS,
	}, cache)
	if err != nil {
		return err
	}
	if recorded != nil {
		*recorded = TrackedFile{FileID: fileID, VersionID: afterVersionID}
	}
	ordinal, err := cache.advanceOrdinal(ctx, q, in.ProjectID)
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
	if err := transitionDirectoryWithCache(ctx, q, in.RecordInput, ordinal, cache); err != nil {
		return err
	}
	if err := upsertSourceHeadWithCache(ctx, q, db.SourceBranchHeads{
		ProjectID: in.ProjectID, BranchID: in.BranchID.String(),
		FileID: fileID, VersionID: afterVersionID,
		RootID: in.RootID, Path: in.Path, State: state, ContentSha256: afterSHA,
		Ordinal: ordinal, ObservedTs: in.TS.Format(time.RFC3339Nano),
	}, cache); err != nil {
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
	if err := updateLineAttributionWithState(ctx, q, in.RecordInput, fileID, effectID, isNewFile); err != nil {
		return err
	}

	return nil
}

func updateLineAttributionWithState(
	ctx context.Context,
	q *db.Queries,
	in RecordInput,
	fileID, effectID string,
	isNewFile bool,
) error {
	if in.Origin == api.SourceChangeOriginExternal || in.Op == api.SourceChangeOpDelete {
		if isNewFile {
			return nil
		}
		return q.DeleteSourceLineAttrForFile(ctx, db.DeleteSourceLineAttrForFileParams{
			ProjectID: in.ProjectID, BranchID: in.BranchID.String(), FileID: fileID,
		})
	}
	beforeText, beforeOK := "", true
	if in.Before != nil {
		beforeText, beforeOK = decodeTextSnapshot(in.Before)
	}
	afterText, afterOK := decodeTextSnapshot(in.After)
	var intervals []Interval
	if !isNewFile {
		existing, err := q.ListSourceLineAttrForFile(ctx, db.ListSourceLineAttrForFileParams{
			ProjectID: in.ProjectID, BranchID: in.BranchID.String(), FileID: fileID,
		})
		if err != nil {
			return err
		}
		intervals = make([]Interval, 0, len(existing))
		for _, row := range existing {
			intervals = append(intervals, Interval{
				Start: int(row.StartLine), End: int(row.EndLine), ChangeID: row.EffectID,
			})
		}
	}
	var next []Interval
	if beforeOK && afterOK {
		next = ApplyAttribution(intervals, beforeText, afterText, effectID)
	}
	if !isNewFile {
		if err := q.DeleteSourceLineAttrForFile(ctx, db.DeleteSourceLineAttrForFileParams{
			ProjectID: in.ProjectID, BranchID: in.BranchID.String(), FileID: fileID,
		}); err != nil {
			return err
		}
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
