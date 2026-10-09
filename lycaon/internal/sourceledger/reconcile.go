package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
)

// RootSpec identifies one attached project folder for inventory reconciliation.
type RootSpec struct {
	BranchID sourcebranch.ID
	ID       string
	Path     string
}

// cleanRootPath is the canonical spelling snapshots key a root by.
func cleanRootPath(path string) string {
	return filepath.Clean(strings.TrimSpace(path))
}

type observedFile struct {
	rootID string
	path   string
	sha    string
	bytes  []byte
	size   int64
}

// observationCause links drift to its batch and optional ref movement and command window.
type observationCause struct {
	actor           *Contributor
	batchID         string
	gitTransitionID string
	window          *openCommandWindow
}

// apply stamps the cause onto one record; a window lends its chat affiliation
// so the effect answers the turn and session lenses.
func (c observationCause) apply(in RecordInput) RecordInput {
	in.BatchID = c.batchID
	in.GitTransitionID = c.gitTransitionID
	in.Origin = api.SourceChangeOriginExternal
	in.CaptureQuality = CaptureReconciled
	if c.actor != nil {
		in.Cause = CauseGitOperation
		in.Origin = c.actor.Origin
		in.SessionID, in.Turn = c.actor.SessionID, c.actor.Turn
		in.ToolCallID, in.ToolName = c.actor.ToolCallID, c.actor.ToolName
		in.JobID = c.actor.JobID
		return in
	}
	if c.window == nil {
		in.Cause = CauseFilesystemReconcile
		in.ActorLabel = "Outside app"
		return in
	}
	in.Cause = CauseCommandWindow
	in.CommandWindowID = c.window.id
	in.SessionID = c.window.sessionID
	in.Turn = c.window.turn
	in.ToolCallID = c.window.toolCallID
	in.ToolName = c.window.toolName
	return in
}

func (s *Inventory) hasTrackingCheckpoint(ctx context.Context, projectID string) (bool, error) {
	_, err := s.queries.FindSourceCheckpointByKind(ctx, db.FindSourceCheckpointByKindParams{
		ProjectID: projectID, Kind: CheckpointTracking,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// recordObservation records one tracked head's drift and reports whether it
// landed; a head another writer already moved is left alone.
func (s *Inventory) recordObservation(
	ctx context.Context,
	projectID string,
	head db.SourceBranchHeads,
	after *observedFile,
	transactionID string,
	cause observationCause,
) (bool, error) {
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := s.writer.mutationObservationScope(ctx, tx, projectID)
	if err != nil {
		return false, err
	}
	landed, err := s.recordObservationTx(ctx, tx, scope, projectID, head, after, transactionID, cause)
	if err != nil || !landed {
		return landed, err
	}
	return true, tx.Commit()
}

// recordObservationTx is recordObservation inside a caller's transaction,
// which a batch of path observations shares.
func (s *Inventory) recordObservationTx(
	ctx context.Context,
	tx *sql.Tx,
	scope MutationObservationScope,
	projectID string,
	head db.SourceBranchHeads,
	after *observedFile,
	transactionID string,
	cause observationCause,
) (bool, error) {
	if scope != nil && scope.Pending(sourcebranch.ID(head.BranchID), head.RootID, head.Path) {
		return false, nil
	}
	q := s.queries.WithTx(tx)
	current, err := q.GetSourceBranchHeadByFile(ctx, db.GetSourceBranchHeadByFileParams{
		ProjectID: projectID, BranchID: head.BranchID, FileID: head.FileID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current.State != head.State || current.ContentSha256 != head.ContentSha256 ||
		current.VersionID != head.VersionID || current.Ordinal != head.Ordinal {
		return false, nil
	}

	op := api.SourceChangeOpWrite
	var afterBytes []byte
	afterSHA := ""
	if after == nil {
		op = api.SourceChangeOpDelete
	} else {
		afterBytes = after.bytes
		afterSHA = after.sha
		if head.State == "absent" || head.VersionID == "" && head.ContentSha256 == "" {
			op = api.SourceChangeOpCreate
		}
	}
	var beforeBytes []byte
	if head.ContentSha256 != "" {
		if raw, ok, err := s.retention.readVerifiedBlob(ctx, head.ContentSha256); err != nil {
			return false, err
		} else if ok {
			beforeBytes = raw
		}
	}
	in := cause.apply(RecordInput{
		ProjectID: projectID, BranchID: sourcebranch.ID(head.BranchID),
		RootID: head.RootID, Path: head.Path, FileID: head.FileID,
		Op:           op,
		BeforeSHA256: head.ContentSha256, AfterSHA256: afterSHA,
		Before: beforeBytes, After: afterBytes,
		BeforeSize: int64(len(beforeBytes)), AfterSize: func() int64 {
			if after == nil {
				return 0
			}
			return after.size
		}(),
		OperationID: transactionID,
	})
	if err := validateBatch([]RecordInput{in}); err != nil {
		return false, err
	}
	if err := s.writer.recordBatchTx(ctx, q, []RecordInput{in}); err != nil {
		return false, err
	}
	return true, nil
}

// recordWindowAdmissions lands a window's admitted files as one operation per
// git cause.
func (s *Inventory) recordWindowAdmissions(ctx context.Context, inputs []RecordInput) (int, error) {
	if len(inputs) == 0 {
		return 0, nil
	}
	byCause := make(map[string][]RecordInput)
	order := make([]string, 0, 2)
	for _, in := range inputs {
		if _, seen := byCause[in.GitTransitionID]; !seen {
			order = append(order, in.GitTransitionID)
		}
		byCause[in.GitTransitionID] = append(byCause[in.GitTransitionID], in)
	}
	recorded := 0
	for _, key := range order {
		batch := byCause[key]
		landed, err := s.recordObservedBatch(ctx, batch)
		if err != nil {
			return recorded, err
		}
		recorded += landed
	}
	return recorded, nil
}

func (s *Inventory) recordObservedBatch(ctx context.Context, inputs []RecordInput) (int, error) {
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := s.writer.mutationObservationScope(ctx, tx, inputs[0].ProjectID)
	if err != nil {
		return 0, err
	}
	admitted := make([]RecordInput, 0, len(inputs))
	for _, in := range inputs {
		if scope == nil || !scope.Pending(in.BranchID, in.RootID, in.Path) {
			admitted = append(admitted, in)
		}
	}
	if len(admitted) == 0 {
		return 0, nil
	}
	if err := s.writer.RecordBatchTx(ctx, tx, admitted); err != nil {
		return 0, err
	}
	return len(admitted), tx.Commit()
}
