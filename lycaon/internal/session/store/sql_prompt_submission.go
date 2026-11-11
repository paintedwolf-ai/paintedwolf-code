package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
)

// PutPromptSubmission inserts or replays one admission receipt.
func (sub *sqlSubmissions) PutPromptSubmission(ctx context.Context, in PromptSubmission) (*PromptSubmission, bool, error) {
	if sub == nil {
		return nil, false, fmt.Errorf("prompt submission store unavailable")
	}
	return putPromptSubmissionSQL(ctx, sub.core, in)
}

func putPromptSubmissionSQL(ctx context.Context, s *sqlCore, in PromptSubmission) (*PromptSubmission, bool, error) {
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

// GetPromptSubmission reads one admission receipt.
func (sub *sqlSubmissions) GetPromptSubmission(ctx context.Context, id string) (*PromptSubmission, error) {
	row, err := sub.core.queries.GetPromptSubmission(ctx, id)
	if db.IsNoRows(err) {
		return nil, ErrPromptSubmissionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get prompt submission: %w", err)
	}
	return promptSubmissionFromRow(row)
}

func (sub *sqlSubmissions) ListQueuedUserPromptSubmissions(ctx context.Context, sessionID string) ([]PromptSubmission, error) {
	rows, err := sub.core.queries.ListQueuedUserPromptSubmissionsBySession(ctx, sessionID)
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
func (sub *sqlSubmissions) ListUnsettledUserPromptSubmissionIDs(ctx context.Context, sessionID string) ([]string, error) {
	ids, err := sub.core.queries.ListUnsettledUserPromptSubmissionIDs(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list unsettled user prompt submissions: %w", err)
	}
	return ids, nil
}

// ClaimPromptSubmission uses status and token fences.
func (sub *sqlSubmissions) ClaimPromptSubmission(ctx context.Context, id string) (*PromptSubmission, bool, error) {
	if sub == nil {
		return nil, false, fmt.Errorf("prompt submission store unavailable")
	}
	return claimPromptSubmissionSQL(ctx, sub.core, id)
}

func claimPromptSubmissionSQL(ctx context.Context, s *sqlCore, id string) (*PromptSubmission, bool, error) {
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
func (sub *sqlSubmissions) ClaimPromptSubmissions(ctx context.Context, ids []string) ([]PromptSubmission, bool, error) {
	if sub == nil {
		return nil, false, fmt.Errorf("prompt submission store unavailable")
	}
	return claimPromptSubmissionsSQL(ctx, sub.core, ids)
}

func claimPromptSubmissionsSQL(ctx context.Context, s *sqlCore, ids []string) ([]PromptSubmission, bool, error) {
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
func (sub *sqlSubmissions) FinishPromptSubmission(ctx context.Context, id, claimToken string, status PromptSubmissionStatus, resultJSON string, failure PromptSubmissionFailure) error {
	if status != PromptSubmissionComplete && status != PromptSubmissionFailed && status != PromptSubmissionInterrupted {
		return fmt.Errorf("invalid terminal prompt submission status %q", status)
	}
	rows, err := sub.core.queries.FinishPromptSubmission(ctx, db.FinishPromptSubmissionParams{
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
func (sub *sqlSubmissions) UpdateQueuedPromptSubmissionInputs(
	ctx context.Context,
	updates []PromptSubmissionInputUpdate,
) (bool, error) {
	s := sub.core
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
func (sub *sqlSubmissions) CancelQueuedPromptSubmissions(ctx context.Context, ids []string) error {
	s := sub.core
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
