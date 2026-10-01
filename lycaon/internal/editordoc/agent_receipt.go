package editordoc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

func agentInputDigest(in AgentEdit) (string, error) {
	encoded, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func recordAgentReceipt(ctx context.Context, tx *sql.Tx, id string, in AgentEdit, held string, publish bool) error {
	digest, err := agentInputDigest(in)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO editor_agent_receipts
 (document_id,operation_id,input_digest,held_version_id,publication_requested)
 VALUES(?,?,?,?,?) ON CONFLICT(document_id,operation_id) DO UPDATE SET
 held_version_id=excluded.held_version_id,publication_requested=excluded.publication_requested`,
		id, in.OperationID, digest, held, publish)
	return err
}

// Retry admission precedes anchor resolution: an accepted edit may have removed
// its own anchors, and another replica may already have edited its result.
func (s *Service) replayAgentEdit(ctx context.Context, d *Document, in AgentEdit) (*AgentEditResult, error) {
	var digest, held, publicationError string
	var publish bool
	err := s.store.db.QueryRowContext(ctx, `SELECT input_digest,held_version_id,publication_requested,publication_error
 FROM editor_agent_receipts WHERE document_id=? AND operation_id=?`, d.ID, in.OperationID).Scan(&digest, &held, &publish, &publicationError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	inputDigest, err := agentInputDigest(in)
	if err != nil {
		return nil, err
	}
	if digest != inputDigest {
		return nil, ErrOperationConflict
	}
	saved := !publish && held == ""
	if publish {
		mutation, err := s.store.Mutation(ctx, in.OperationID)
		if err != nil {
			return nil, err
		}
		if mutation.Status == "prepared" || mutation.Status == "file_applied" {
			p, err := s.projectBranch(ctx, d.ProjectID, d.BranchID)
			if err != nil {
				return nil, err
			}
			if err := s.recoverMutation(ctx, p, mutation); err != nil {
				return nil, err
			}
			mutation, err = s.store.Mutation(ctx, in.OperationID)
			if err != nil {
				return nil, err
			}
		}
		saved = mutation.Status == "complete"
		if err := s.store.db.QueryRowContext(ctx, `SELECT held_version_id,publication_error FROM editor_agent_receipts WHERE document_id=? AND operation_id=?`, d.ID, in.OperationID).Scan(&held, &publicationError); err != nil {
			return nil, err
		}
	}
	current, err := s.store.Get(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	if err := s.replicaProjection(ctx, current, nil); err != nil {
		return nil, err
	}
	result := &AgentEditResult{Document: s.withParticipants(current), Saved: saved, HeldVersionID: held}
	if publicationError != "" {
		result.PublicationError = errors.New(publicationError)
	}
	return result, nil
}
