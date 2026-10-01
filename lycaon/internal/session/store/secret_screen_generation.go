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
func (s *SQL) SessionTreeIDs(ctx context.Context, rootSessionID string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.queries.ListSessionTreeIDs(ctx, rootSessionID)
}

// MessageIDsBelowScreenGeneration returns rows last screened before generation.
func (s *SQL) MessageIDsBelowScreenGeneration(ctx context.Context, sessionID string, generation uint64) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.queries.ListMessageIDsBelowScreenGeneration(ctx, db.ListMessageIDsBelowScreenGenerationParams{
		SessionID:              sessionID,
		SecretScreenGeneration: storedGeneration(generation),
	})
}

// MaxMessageScreenGenerationInTree returns the highest tree revision stamp. A
// sweep works above it, so the sequence keeps rising across restarts of the
// in-memory revision counter.
func (s *SQL) MaxMessageScreenGenerationInTree(ctx context.Context, rootSessionID string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	stamped, err := s.queries.MaxMessageScreenGenerationInTree(ctx, rootSessionID)
	if err != nil {
		return 0, err
	}
	if stamped < 0 {
		return 0, nil
	}
	return uint64(stamped), nil
}

// StampMessageScreenGeneration records the revision a row was screened at.
func (s *SQL) StampMessageScreenGeneration(ctx context.Context, sessionID, messageID string, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.queries.StampMessageScreenGeneration(ctx, db.StampMessageScreenGenerationParams{
		SecretScreenGeneration: storedGeneration(generation),
		SessionID:              sessionID,
		ID:                     messageID,
	})
}

// SessionTreeIDs walks the in-memory parent links.
func (s *Memory) SessionTreeIDs(ctx context.Context, rootSessionID string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return memorySessionTreeIDs(s.sessions, rootSessionID), nil
}

// MessageIDsBelowScreenGeneration returns rows last screened before generation.
func (s *Memory) MessageIDsBelowScreenGeneration(ctx context.Context, sessionID string, generation uint64) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, msg := range s.messages[sessionID] {
		if s.screenGeneration[sessionID][msg.ID] < generation {
			out = append(out, msg.ID)
		}
	}
	return out, nil
}

// MaxMessageScreenGenerationInTree returns the highest revision stamped on any
// row in the tree.
func (s *Memory) MaxMessageScreenGenerationInTree(ctx context.Context, rootSessionID string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var max uint64
	for _, sessionID := range memorySessionTreeIDs(s.sessions, rootSessionID) {
		for _, generation := range s.screenGeneration[sessionID] {
			if generation > max {
				max = generation
			}
		}
	}
	return max, nil
}

// StampMessageScreenGeneration records the revision a row was screened at.
func (s *Memory) StampMessageScreenGeneration(ctx context.Context, sessionID, messageID string, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.screenGeneration[sessionID] == nil {
		s.screenGeneration[sessionID] = map[string]uint64{}
	}
	s.screenGeneration[sessionID][messageID] = generation
	return nil
}
