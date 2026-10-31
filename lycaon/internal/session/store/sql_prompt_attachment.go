package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// ListOperationPromptAttachmentRetentions returns one operation's claims.
func (a *sqlAttachmentBlobs) ListOperationPromptAttachmentRetentions(ctx context.Context, operationID string) ([]PromptAttachmentRetention, error) {
	rows, err := a.core.queries.ListOperationPromptAttachmentRetentions(ctx, operationID)
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
func (a *sqlAttachmentBlobs) PromptAttachmentBlobRetained(ctx context.Context, projectID, blobID string) (bool, error) {
	retained, err := a.core.queries.PromptAttachmentBlobRetained(ctx, db.PromptAttachmentBlobRetainedParams{
		TargetProjectID: strings.TrimSpace(projectID),
		TargetBlobID:    strings.TrimSpace(blobID),
	})
	if err != nil {
		return false, fmt.Errorf("check prompt attachment retention: %w", err)
	}
	return retained != 0, nil
}

// RecordPromptAttachmentBlob records one materialized body.
func (a *sqlAttachmentBlobs) RecordPromptAttachmentBlob(ctx context.Context, projectID, blobID string, byteSize int64) error {
	if byteSize < 0 {
		return fmt.Errorf("record prompt attachment blob: negative byte size")
	}
	err := a.core.queries.UpsertPromptAttachmentBlob(ctx, db.UpsertPromptAttachmentBlobParams{
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
func (a *sqlAttachmentBlobs) DeletePromptAttachmentBlob(ctx context.Context, projectID, blobID string) error {
	s := a.core
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
func (a *sqlAttachmentBlobs) ListPromptAttachmentReclaimCandidates(
	ctx context.Context, projectID string, createdBefore time.Time, limit int,
) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := a.core.queries.ListPromptAttachmentReclaimCandidates(ctx, db.ListPromptAttachmentReclaimCandidatesParams{
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
func (a *sqlAttachmentBlobs) PromptAttachmentStorageUsage(ctx context.Context, projectID string) (int64, error) {
	total, err := a.core.queries.SumProjectPromptAttachmentBytes(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return 0, fmt.Errorf("sum prompt attachment bytes: %w", err)
	}
	return total, nil
}

func (sub *sqlSubmissions) InterruptPromptSubmissionsBySession(ctx context.Context, sessionID string) error {
	s := sub.core
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
func (sub *sqlSubmissions) InterruptRunningPromptSubmissionsBySession(ctx context.Context, sessionID string) error {
	s := sub.core
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
func (sub *sqlSubmissions) RecoverPromptSubmissions(ctx context.Context) ([]string, error) {
	return recoverPromptSubmissionsSQL(ctx, sub.core)
}

func recoverPromptSubmissionsSQL(ctx context.Context, s *sqlCore) ([]string, error) {
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
