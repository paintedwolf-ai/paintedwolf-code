package watch

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

type PresentationKind string

const (
	ClaimDirectWait     PresentationKind = "direct_wait"
	ClaimWatchEvidence  PresentationKind = "watch_evidence"
	ClaimCollectedFrame PresentationKind = "collected_frame"
	ClaimFallbackKick   PresentationKind = "fallback_kick"
)

// PresentationDecision describes how one producer occurrence may be presented to a destination.
type PresentationDecision struct {
	Granted            bool
	TransferredKick    bool
	AlreadyAdmitted    bool
	ExistingClaimKind  PresentationKind
	ExistingReceiptID  string
}

// ClaimPresentation records or transfers a single presentation claim per destination and producer occurrence.
func ClaimPresentation(
	ctx context.Context,
	s *Store,
	owner Owner,
	occurrenceID string,
	kind PresentationKind,
	admitted bool,
	receiptID string,
) (PresentationDecision, error) {
	if !s.Configured() {
		return PresentationDecision{Granted: true}, nil
	}
	owner = owner.Normalized()
	occurrenceID = strings.TrimSpace(occurrenceID)
	if owner.SessionID == "" || occurrenceID == "" {
		return PresentationDecision{Granted: true}, nil
	}
	scope := owner.Destination()
	if owner.WorkerJobID != "" {
		scope = "job:" + owner.WorkerJobID
	}
	nowStr := s.clock.Now().UTC().Format(time.RFC3339Nano)
	desiredState := "queued"
	if admitted {
		desiredState = "admitted"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PresentationDecision{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var existingID, existingKind, existingState, existingReceipt string
	err = tx.QueryRowContext(ctx, `SELECT claim_id, claim_kind, state, receipt_id
		FROM watch_presentation_claims
		WHERE session_id = ? AND destination_scope = ? AND producer_occurrence_id = ?`,
		owner.SessionID, scope, occurrenceID).Scan(&existingID, &existingKind, &existingState, &existingReceipt)
	if errors.Is(err, sql.ErrNoRows) {
		claimID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `INSERT INTO watch_presentation_claims
			(claim_id, session_id, worker_job_id, destination_scope, producer_occurrence_id, claim_kind, state, receipt_id, created_at)
			VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?)`,
			claimID, owner.SessionID, owner.WorkerJobID, scope, occurrenceID,
			string(kind), desiredState, strings.TrimSpace(receiptID), nowStr); err != nil {
			return PresentationDecision{}, err
		}
		if err := tx.Commit(); err != nil {
			return PresentationDecision{}, err
		}
		return PresentationDecision{Granted: true}, nil
	}
	if err != nil {
		return PresentationDecision{}, err
	}

	if existingState == "admitted" {
		return PresentationDecision{
			Granted:           false,
			AlreadyAdmitted:   true,
			ExistingClaimKind: PresentationKind(existingKind),
			ExistingReceiptID: existingReceipt,
		}, nil
	}

	// A queued fallback kick yields its claim to an explicit watch, collected frame or direct wait.
	if existingKind == string(ClaimFallbackKick) && kind != ClaimFallbackKick {
		nextReceipt := strings.TrimSpace(receiptID)
		if nextReceipt == "" {
			nextReceipt = existingReceipt
		}
		if _, err := tx.ExecContext(ctx, `UPDATE watch_presentation_claims
			SET claim_kind = ?, state = ?, receipt_id = ?
			WHERE claim_id = ?`,
			string(kind), desiredState, nextReceipt, existingID); err != nil {
			return PresentationDecision{}, err
		}
		if err := tx.Commit(); err != nil {
			return PresentationDecision{}, err
		}
		return PresentationDecision{Granted: true, TransferredKick: true}, nil
	}

	// A queued watch claim and collected frame share ownership, and queued claims promote in place.
	if kind != ClaimFallbackKick && (existingKind == string(kind) ||
		existingKind == string(ClaimWatchEvidence) ||
		existingKind == string(ClaimCollectedFrame)) {
		nextReceipt := strings.TrimSpace(receiptID)
		if nextReceipt == "" {
			nextReceipt = existingReceipt
		}
		if _, err := tx.ExecContext(ctx, `UPDATE watch_presentation_claims
			SET claim_kind = ?, state = ?, receipt_id = ? WHERE claim_id = ?`,
			string(kind), desiredState, nextReceipt, existingID); err != nil {
			return PresentationDecision{}, err
		}
		if err := tx.Commit(); err != nil {
			return PresentationDecision{}, err
		}
		return PresentationDecision{Granted: true}, nil
	}

	return PresentationDecision{
		Granted:           false,
		ExistingClaimKind: PresentationKind(existingKind),
		ExistingReceiptID: existingReceipt,
	}, nil
}

// MarkPresentationAdmittedByHandle marks any queued fallback kick for handle admitted with receiptID.
func MarkPresentationAdmittedByHandle(ctx context.Context, s *Store, sessionID, handle, receiptID string) error {
	if !s.Configured() || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(handle) == "" {
		return nil
	}
	prefix := "process_done:" + strings.TrimSpace(handle)
	_, err := s.db.ExecContext(ctx, `UPDATE watch_presentation_claims
		SET state = 'admitted', receipt_id = CASE WHEN ? != '' THEN ? ELSE receipt_id END
		WHERE session_id = ? AND claim_kind = 'fallback_kick' AND state = 'queued'
		  AND (producer_occurrence_id = ? OR producer_occurrence_id LIKE ?)`,
		strings.TrimSpace(receiptID), strings.TrimSpace(receiptID),
		strings.TrimSpace(sessionID), prefix, prefix+":%")
	return err
}

// MarkPresentationAdmitted marks a queued claim admitted with its receipt ID.
func MarkPresentationAdmitted(ctx context.Context, s *Store, owner Owner, occurrenceID, receiptID string) error {
	if !s.Configured() {
		return nil
	}
	owner = owner.Normalized()
	scope := owner.Destination()
	if owner.WorkerJobID != "" {
		scope = "job:" + owner.WorkerJobID
	}
	_, err := s.db.ExecContext(ctx, `UPDATE watch_presentation_claims
		SET state = 'admitted', receipt_id = CASE WHEN ? != '' THEN ? ELSE receipt_id END
		WHERE session_id = ? AND destination_scope = ? AND producer_occurrence_id = ?`,
		strings.TrimSpace(receiptID), strings.TrimSpace(receiptID), owner.SessionID, scope, strings.TrimSpace(occurrenceID))
	return err
}
