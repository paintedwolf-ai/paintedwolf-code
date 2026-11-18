package store

import (
	"context"
	"math"

	"github.com/lycaon/lycaon/internal/db"
)

// storedGeneration narrows the revision to the column's type. Saturating keeps
// the comparison ordered; wrapping would stamp below every row already written.
func storedGeneration(generation uint64) int64 {
	if generation > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(generation)
}

// SessionTreeIDs returns the root and every descendant session. Secret evidence
// is keyed to a tree, so tree-scoped work resolves membership here.
func (sg *sqlScreenGeneration) SessionTreeIDs(ctx context.Context, rootSessionID string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return sg.core.queries.ListSessionTreeIDs(ctx, rootSessionID)
}

// MessageIDsBelowScreenGeneration returns rows last screened before generation.
func (sg *sqlScreenGeneration) MessageIDsBelowScreenGeneration(ctx context.Context, sessionID string, generation uint64) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return sg.core.queries.ListMessageIDsBelowScreenGeneration(ctx, db.ListMessageIDsBelowScreenGenerationParams{
		SessionID:              sessionID,
		SecretScreenGeneration: storedGeneration(generation),
	})
}

// MaxMessageScreenGenerationInTree returns the highest tree revision stamp. A
// sweep works above it, so the sequence keeps rising across restarts of the
// in-memory revision counter.
func (sg *sqlScreenGeneration) MaxMessageScreenGenerationInTree(ctx context.Context, rootSessionID string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	stamped, err := sg.core.queries.MaxMessageScreenGenerationInTree(ctx, rootSessionID)
	if err != nil {
		return 0, err
	}
	if stamped < 0 {
		return 0, nil
	}
	return uint64(stamped), nil
}

// StampMessageScreenGeneration records the revision a row was screened at.
func (sg *sqlScreenGeneration) StampMessageScreenGeneration(ctx context.Context, sessionID, messageID string, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return sg.core.queries.StampMessageScreenGeneration(ctx, db.StampMessageScreenGenerationParams{
		SecretScreenGeneration: storedGeneration(generation),
		SessionID:              sessionID,
		ID:                     messageID,
	})
}
