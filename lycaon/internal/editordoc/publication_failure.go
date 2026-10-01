package editordoc

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/sourceledger"
)

// Terminal publication and its retained agent bytes commit together. An intent
// whose filesystem effect still needs attribution stays pending for recovery.
func (s *Service) settlePublicationFailure(ctx context.Context, d *Document, m *Mutation, status string, cause error) error {
	next, failed := *d, *m
	next.replicaCommit = nil
	failed.Status, failed.Error, failed.UpdatedAt = status, cause.Error(), time.Now().UTC()
	if status == "conflict" {
		next.Diverged = true
	}
	err := s.store.Tx(ctx, func(tx *sql.Tx) error {
		var held string
		err := tx.QueryRowContext(ctx, `SELECT held_version_id FROM editor_agent_receipts WHERE document_id=? AND operation_id=?`, d.ID, m.ID).Scan(&held)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if held == "" {
				held, err = s.ledger.RecordHeldEditTx(ctx, tx, sourceledger.HeldEdit{
					BranchID:  d.BranchID,
					ProjectID: m.ProjectID, FileID: m.FileID, RootID: m.RootID, Path: m.Path,
					Content: m.AfterBytes, SHA256: m.AfterSHA256, Size: int64(len(m.AfterBytes)),
					SessionID: m.SessionID, Turn: m.Turn, ToolCallID: m.ToolCallID, ToolName: m.ToolName, TS: m.CreatedAt,
				})
				if err != nil {
					return err
				}
			}
			next.HeldAgentVersionID = held
			publicationError := ""
			if status != "conflict" {
				publicationError = cause.Error()
			}
			if _, err := tx.ExecContext(ctx, `UPDATE editor_agent_receipts SET held_version_id=?,publication_requested=0,publication_error=? WHERE document_id=? AND operation_id=?`, held, publicationError, d.ID, m.ID); err != nil {
				return err
			}
		}
		if next.Diverged != d.Diverged || next.HeldAgentVersionID != d.HeldAgentVersionID {
			next.Revision++
			next.UpdatedAt = failed.UpdatedAt
			if err := s.store.UpdateCASTx(ctx, tx, &next, d.Revision); err != nil {
				return err
			}
		}
		failed.Checkpoint = nil
		failed.BeforeCheckpoint = nil
		failed.Content, failed.BeforeBytes, failed.AfterBytes = "", nil, nil
		return s.store.UpdateMutationTx(ctx, tx, &failed)
	})
	if err != nil {
		return err
	}
	changed := next.Revision != d.Revision
	*d, *m = next, failed
	if changed {
		s.changed(ctx, d, false)
	}
	return nil
}
