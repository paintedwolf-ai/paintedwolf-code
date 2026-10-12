package hitl

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// Review keys describe held operations, never granted authority.
func ApprovalReviewKeys(req CheckpointRequest) []string {
	keys := slices.Clone(req.ReviewKeys)
	if req.CoalesceGrantKey != "" {
		keys = append(keys, req.CoalesceGrantKey)
	} else if req.ProposedAction != nil {
		keys = append(keys, GrantKey(*req.ProposedAction))
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	return slices.DeleteFunc(keys, func(key string) bool { return key == "" })
}

func PreparedReviewKeys(reviews []*PreparedApproval) []string {
	var keys []string
	for _, review := range reviews {
		keys = append(keys, ApprovalReviewKeys(review.Request)...)
	}
	return keys
}

// Capture and insertion share the writer transaction with user-message admission.
func (s *SQLStore) captureReviewIntent(ctx context.Context, tx *sql.Tx, row *StoredCheckpoint) error {
	if row.Kind != api.CheckpointKindToolApproval && row.Kind != api.CheckpointKindContentApply {
		return nil
	}
	intent, err := s.queries.WithTx(tx).CurrentApprovalReviewIntent(ctx, row.SessionID)
	if err != nil {
		return fmt.Errorf("capture approval review intent: %w", err)
	}
	if row.Payload == nil {
		row.Payload = map[string]any{}
	}
	if keys, ok := row.Payload["review_keys"].([]string); ok {
		opaque := make([]string, len(keys))
		for i, key := range keys {
			opaque[i] = reviewKeyDigest(key)
		}
		row.Payload["review_keys"] = opaque
	}
	row.Payload["review_chat"] = intent.ChatID
	row.Payload["review_message"] = intent.MessageID
	row.Payload["review_direction"] = intent.DirectionID
	return nil
}

// Suppression follows the persisted intent identity across workers and restart.
func (s *SQLStore) ApprovalReviewSuppressed(ctx context.Context, sessionID, key string) (bool, error) {
	suppressed, err := s.queries.ApprovalReviewSuppressed(ctx, db.ApprovalReviewSuppressedParams{
		SessionID: sessionID, ReviewKey: reviewKeyDigest(key),
	})
	if err != nil {
		return false, fmt.Errorf("read approval review suppression: %w", err)
	}
	return suppressed != 0, nil
}

func (m *Checkpoints) ApprovalReviewSuppressed(ctx context.Context, sessionID, key string) (bool, error) {
	return m.Store.ApprovalReviewSuppressed(ctx, sessionID, key)
}

// Workflow denial state follows the current intent.
func (m *Checkpoints) SessionApprovalDenied(ctx context.Context, sessionID string) (bool, error) {
	return m.Store.ApprovalReviewSuppressed(ctx, sessionID, "")
}

func (s *SQLStore) ApprovalReviewIntent(ctx context.Context, sessionID string) (string, error) {
	intent, err := s.queries.CurrentApprovalReviewIntent(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("read approval review intent: %w", err)
	}
	encoded, err := json.Marshal(intent)
	return string(encoded), err
}

func (m *Checkpoints) ApprovalReviewIntent(ctx context.Context, sessionID string) (string, error) {
	return m.Store.ApprovalReviewIntent(ctx, sessionID)
}

// Operation keys may contain paths; persisted identity contains no subject text.
func reviewKeyDigest(key string) string {
	if key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}
