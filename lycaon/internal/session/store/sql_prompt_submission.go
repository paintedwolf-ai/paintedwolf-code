package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
)

// PutPromptSubmission inserts or replays one admission receipt.
func (s *SQL) PutPromptSubmission(ctx context.Context, in PromptSubmission) (*PromptSubmission, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, fmt.Errorf("prompt submission store unavailable")
	}
	createdAt := in.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	if err := in.validateAuthorship(); err != nil {
		return nil, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin prompt submission: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
	rows, err := qtx.InsertPromptSubmission(ctx, db.InsertPromptSubmissionParams{
		ID: in.ID, SessionID: in.SessionID, ProjectID: in.ProjectID,
		InputDigest: in.InputDigest, InputJson: in.InputJSON, CreatedAt: createdAt.Format(time.RFC3339Nano),
		Origin: string(in.Origin), SubmittedBy: db.NullString(in.SubmittedBy),
	})
	if err != nil {
		return nil, false, fmt.Errorf("insert prompt submission: %w", err)
	}
	rowData, err := qtx.GetPromptSubmission(ctx, in.ID)
	if err != nil {
		return nil, false, err
	}
	row, err := promptSubmissionFromRow(rowData)
	if err != nil {
		return nil, false, err
	}
	if !row.replays(in) {
		return nil, false, &PromptSubmissionConflictError{ID: in.ID}
	}
	if rows == 1 {
		for _, blobID := range normalizedBlobIDs(in.AttachmentBlobIDs) {
			if err := qtx.InsertPromptAttachmentAdmission(ctx, db.InsertPromptAttachmentAdmissionParams{
				SubmissionID: in.ID, ProjectID: in.ProjectID, BlobID: blobID,
			}); err != nil {
				return nil, false, fmt.Errorf("retain prompt attachment: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit prompt submission: %w", err)
	}
	return row, rows == 1, nil
}

func normalizedBlobIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// ListOperationPromptAttachmentRetentions returns one operation's claims.
func (s *SQL) ListOperationPromptAttachmentRetentions(ctx context.Context, operationID string) ([]PromptAttachmentRetention, error) {
	rows, err := s.queries.ListOperationPromptAttachmentRetentions(ctx, operationID)
	if err != nil {
		return nil, fmt.Errorf("list operation prompt attachment retentions: %w", err)
	}
	out := make([]PromptAttachmentRetention, 0, len(rows))
	for _, row := range rows {
		out = append(out, PromptAttachmentRetention{
			ProjectID: row.ProjectID, OperationID: row.OperationID, BlobID: row.BlobID,
		})
	}
	return out, nil
}

// PromptAttachmentBlobRetained reports whether durable history claims a blob.
func (s *SQL) PromptAttachmentBlobRetained(ctx context.Context, projectID, blobID string) (bool, error) {
	retained, err := s.queries.PromptAttachmentBlobRetained(ctx, db.PromptAttachmentBlobRetainedParams{
		TargetProjectID: strings.TrimSpace(projectID),
		TargetBlobID:    strings.TrimSpace(blobID),
	})
	if err != nil {
		return false, fmt.Errorf("check prompt attachment retention: %w", err)
	}
	return retained != 0, nil
}

// RecordPromptAttachmentBlob records one materialized body.
func (s *SQL) RecordPromptAttachmentBlob(ctx context.Context, projectID, blobID string, byteSize int64) error {
	if byteSize < 0 {
		return fmt.Errorf("record prompt attachment blob: negative byte size")
	}
	err := s.queries.UpsertPromptAttachmentBlob(ctx, db.UpsertPromptAttachmentBlobParams{
		ProjectID: strings.TrimSpace(projectID),
		BlobID:    strings.TrimSpace(blobID),
		ByteSize:  byteSize,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return fmt.Errorf("record prompt attachment blob: %w", err)
	}
	return nil
}

// DeletePromptAttachmentBlob removes one materialized-body record.
func (s *SQL) DeletePromptAttachmentBlob(ctx context.Context, projectID, blobID string) error {
	projectID = strings.TrimSpace(projectID)
	blobID = strings.TrimSpace(blobID)
	rows, err := s.queries.DeletePromptAttachmentBlob(ctx, db.DeletePromptAttachmentBlobParams{
		ProjectID: projectID, BlobID: blobID,
	})
	if err != nil {
		return fmt.Errorf("delete prompt attachment blob: %w", err)
	}
	if rows == 0 {
		exists, existsErr := s.queries.PromptAttachmentBlobExists(ctx, db.PromptAttachmentBlobExistsParams{
			ProjectID: projectID, BlobID: blobID,
		})
		if existsErr != nil {
			return fmt.Errorf("inspect prompt attachment blob: %w", existsErr)
		}
		if exists != 0 {
			return ErrPromptAttachmentRetained
		}
	}
	return nil
}

// ListPromptAttachmentReclaimCandidates returns an aged bounded batch.
func (s *SQL) ListPromptAttachmentReclaimCandidates(
	ctx context.Context, projectID string, createdBefore time.Time, limit int,
) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.queries.ListPromptAttachmentReclaimCandidates(ctx, db.ListPromptAttachmentReclaimCandidatesParams{
		ProjectID:     strings.TrimSpace(projectID),
		CreatedBefore: createdBefore.UTC().Format(time.RFC3339Nano),
		BatchLimit:    int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list prompt attachment reclaim candidates: %w", err)
	}
	return rows, nil
}

// PromptAttachmentStorageUsage reports recorded project bytes.
func (s *SQL) PromptAttachmentStorageUsage(ctx context.Context, projectID string) (int64, error) {
	total, err := s.queries.SumProjectPromptAttachmentBytes(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return 0, fmt.Errorf("sum prompt attachment bytes: %w", err)
	}
	return total, nil
}

// GetPromptSubmission reads one admission receipt.
func (s *SQL) GetPromptSubmission(ctx context.Context, id string) (*PromptSubmission, error) {
	row, err := s.queries.GetPromptSubmission(ctx, id)
	if db.IsNoRows(err) {
		return nil, ErrPromptSubmissionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get prompt submission: %w", err)
	}
	return promptSubmissionFromRow(row)
}

func (s *SQL) ListQueuedUserPromptSubmissions(ctx context.Context, sessionID string) ([]PromptSubmission, error) {
	rows, err := s.queries.ListQueuedUserPromptSubmissionsBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list queued user prompt submissions: %w", err)
	}
	out := make([]PromptSubmission, 0, len(rows))
	for _, row := range rows {
		converted, convertErr := promptSubmissionFromValues(
			row.ID, row.SessionID, row.AdmissionSeq, row.ProjectID, row.InputDigest, row.InputJson, row.Status,
			row.ClaimToken, row.ResultJson, PromptSubmissionFailure{Message: row.Error, Code: row.ErrorCode},
			row.CreatedAt, row.StartedAt, row.CompletedAt, row.Origin, row.SubmittedBy,
		)
		if convertErr != nil {
			return nil, convertErr
		}
		out = append(out, *converted)
	}
	return out, nil
}

// ListUnsettledUserPromptSubmissionIDs lists human prompts that are queued or
// running, in admission order.
func (s *SQL) ListUnsettledUserPromptSubmissionIDs(ctx context.Context, sessionID string) ([]string, error) {
	ids, err := s.queries.ListUnsettledUserPromptSubmissionIDs(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list unsettled user prompt submissions: %w", err)
	}
	return ids, nil
}

// ClaimPromptSubmission uses status and token fences.
func (s *SQL) ClaimPromptSubmission(ctx context.Context, id string) (*PromptSubmission, bool, error) {
	if s == nil || s.db == nil || s.queries == nil {
		return nil, false, fmt.Errorf("prompt submission store unavailable")
	}
	claimToken := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin prompt submission claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	rows, err := queries.ClaimPromptSubmission(ctx, db.ClaimPromptSubmissionParams{
		ClaimToken: db.NullString(claimToken), StartedAt: db.NullString(now), ID: id,
	})
	if err != nil {
		return nil, false, fmt.Errorf("claim prompt submission: %w", err)
	}
	rowData, err := queries.GetPromptSubmission(ctx, id)
	if err != nil {
		return nil, false, err
	}
	row, err := promptSubmissionFromRow(rowData)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit prompt submission claim: %w", err)
	}
	return row, rows == 1, nil
}

// ClaimPromptSubmissions atomically claims one queue turn. Either every receipt moves to
// running with its own fencing token or none does.
func (s *SQL) ClaimPromptSubmissions(ctx context.Context, ids []string) ([]PromptSubmission, bool, error) {
	if s == nil || s.db == nil || s.queries == nil {
		return nil, false, fmt.Errorf("prompt submission store unavailable")
	}
	if len(ids) == 0 {
		return []PromptSubmission{}, true, nil
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, false, fmt.Errorf("claim prompt submissions: empty id")
		}
		if _, exists := seen[id]; exists {
			return nil, false, fmt.Errorf("claim prompt submissions: duplicate id %s", id)
		}
		seen[id] = struct{}{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin prompt submission claims: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	out := make([]PromptSubmission, 0, len(ids))
	for _, id := range ids {
		claimToken := uuid.NewString()
		now := time.Now().UTC().Format(time.RFC3339Nano)
		rows, claimErr := queries.ClaimPromptSubmission(ctx, db.ClaimPromptSubmissionParams{
			ClaimToken: db.NullString(claimToken), StartedAt: db.NullString(now), ID: id,
		})
		if claimErr != nil {
			return nil, false, fmt.Errorf("claim prompt submission %s: %w", id, claimErr)
		}
		if rows != 1 {
			return nil, false, nil
		}
		row, getErr := queries.GetPromptSubmission(ctx, id)
		if getErr != nil {
			return nil, false, fmt.Errorf("read claimed prompt submission %s: %w", id, getErr)
		}
		converted, convertErr := promptSubmissionFromRow(row)
		if convertErr != nil {
			return nil, false, convertErr
		}
		out = append(out, *converted)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit prompt submission claims: %w", err)
	}
	return out, true, nil
}

// FinishPromptSubmission closes a matching fenced claim.
func (s *SQL) FinishPromptSubmission(ctx context.Context, id, claimToken string, status PromptSubmissionStatus, resultJSON string, failure PromptSubmissionFailure) error {
	if status != PromptSubmissionComplete && status != PromptSubmissionFailed && status != PromptSubmissionInterrupted {
		return fmt.Errorf("invalid terminal prompt submission status %q", status)
	}
	rows, err := s.queries.FinishPromptSubmission(ctx, db.FinishPromptSubmissionParams{
		Status: string(status), ResultJson: db.NullString(resultJSON),
		Error: failure.Message, ErrorCode: failure.Code,
		CompletedAt: db.NullString(time.Now().UTC().Format(time.RFC3339Nano)),
		ID:          id, ClaimToken: db.NullString(claimToken),
	})
	if err != nil {
		return fmt.Errorf("finish prompt submission: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("prompt submission %s claim lost", id)
	}
	return nil
}

// UpdateQueuedPromptSubmissionInputs updates a receipt set atomically.
func (s *SQL) UpdateQueuedPromptSubmissionInputs(
	ctx context.Context,
	updates []PromptSubmissionInputUpdate,
) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin queued prompt input update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	for _, update := range updates {
		rows, updateErr := queries.UpdateQueuedPromptSubmissionInput(ctx, db.UpdateQueuedPromptSubmissionInputParams{
			InputJson: update.InputJSON,
			ID:        update.ID,
		})
		if updateErr != nil {
			return false, fmt.Errorf("update queued prompt submission %s: %w", update.ID, updateErr)
		}
		if rows != 1 {
			return false, nil
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit queued prompt input update: %w", err)
	}
	return true, nil
}

// CancelQueuedPromptSubmissions resolves one queue edit atomically.
func (s *SQL) CancelQueuedPromptSubmissions(ctx context.Context, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin queued prompt cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range ids {
		if _, err := queries.CancelQueuedPromptSubmission(ctx, db.CancelQueuedPromptSubmissionParams{
			CompletedAt: db.NullString(now),
			ID:          id,
		}); err != nil {
			return fmt.Errorf("cancel queued prompt submission %s: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit queued prompt cancellations: %w", err)
	}
	return nil
}

func (s *SQL) InterruptPromptSubmissionsBySession(ctx context.Context, sessionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin prompt submission interruption: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	now := db.NullString(time.Now().UTC().Format(time.RFC3339Nano))
	if _, err := queries.CancelQueuedPromptSubmissionsBySession(ctx, db.CancelQueuedPromptSubmissionsBySessionParams{
		CompletedAt: now, SessionID: sessionID,
	}); err != nil {
		return fmt.Errorf("cancel queued prompt submissions for session: %w", err)
	}
	if _, err := queries.InterruptRunningPromptSubmissionsBySession(ctx, db.InterruptRunningPromptSubmissionsBySessionParams{
		CompletedAt: now, SessionID: sessionID,
	}); err != nil {
		return fmt.Errorf("interrupt running prompt submissions for session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit prompt submission interruption: %w", err)
	}
	return nil
}

// InterruptRunningPromptSubmissionsBySession interrupts running prompt submissions.
func (s *SQL) InterruptRunningPromptSubmissionsBySession(ctx context.Context, sessionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin running prompt interruption: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	if _, err := queries.InterruptRunningPromptSubmissionsBySession(ctx, db.InterruptRunningPromptSubmissionsBySessionParams{
		CompletedAt: db.NullString(time.Now().UTC().Format(time.RFC3339Nano)), SessionID: sessionID,
	}); err != nil {
		return fmt.Errorf("interrupt running prompt submissions for session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit running prompt interruption: %w", err)
	}
	return nil
}

// RecoverPromptSubmissions returns runnable queued user turns.
func (s *SQL) RecoverPromptSubmissions(ctx context.Context) ([]string, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin prompt recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if err := q.RequeueRunningUserPromptSubmissions(ctx); err != nil {
		return nil, fmt.Errorf("requeue interrupted user prompts: %w", err)
	}
	if err := q.InterruptRecoveringHostPromptSubmissions(ctx, db.NullString(now)); err != nil {
		return nil, fmt.Errorf("interrupt host prompts: %w", err)
	}
	ids, err := q.ListQueuedUserPromptSubmissionIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list queued prompt submissions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit prompt recovery: %w", err)
	}
	return ids, nil
}

func promptSubmissionFromRow(row db.GetPromptSubmissionRow) (*PromptSubmission, error) {
	return promptSubmissionFromValues(
		row.ID, row.SessionID, row.AdmissionSeq, row.ProjectID, row.InputDigest, row.InputJson, row.Status,
		row.ClaimToken, row.ResultJson, PromptSubmissionFailure{Message: row.Error, Code: row.ErrorCode},
		row.CreatedAt, row.StartedAt, row.CompletedAt, row.Origin, row.SubmittedBy,
	)
}

func promptSubmissionFromValues(
	id, sessionID string,
	admissionSeq int64,
	projectID, inputDigest, inputJSON, status, claimToken, resultJSON string,
	failure PromptSubmissionFailure,
	createdAt string,
	startedAt, completedAt sql.NullString,
	origin, submittedBy string,
) (*PromptSubmission, error) {
	out := PromptSubmission{
		ID: id, SessionID: sessionID, AdmissionSeq: admissionSeq, ProjectID: projectID,
		InputDigest: inputDigest, InputJSON: inputJSON, SubmittedBy: submittedBy,
		Origin: PromptSubmissionOrigin(origin), Status: PromptSubmissionStatus(status),
		ClaimToken: claimToken, ResultJSON: resultJSON,
		Error: failure.Message, ErrorCode: failure.Code,
	}
	var err error
	out.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse prompt submission created_at: %w", err)
	}
	if startedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, startedAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse prompt submission started_at: %w", parseErr)
		}
		out.StartedAt = &value
	}
	if completedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, completedAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse prompt submission completed_at: %w", parseErr)
		}
		out.CompletedAt = &value
	}
	return &out, nil
}
