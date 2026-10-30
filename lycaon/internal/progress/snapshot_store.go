package progress

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

func (s *MemoryStore) Snapshot(ctx context.Context, sessionID string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.bySess[normalizeKey(sessionID)]
	return NewSnapshot(record.runID, record.content), nil
}

func (s *MemoryStore) Update(ctx context.Context, sessionID string, change func(Snapshot) (string, error)) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := normalizeKey(sessionID)
	record := s.bySess[key]
	current := NewSnapshot(record.runID, record.content)
	content, err := change(current)
	if err != nil {
		return current, err
	}
	record.content = clampContent(content)
	s.bySess[key] = record
	return NewSnapshot(record.runID, record.content), nil
}

func querySnapshot(ctx context.Context, queries *db.Queries, key string) (Snapshot, error) {
	content, err := queries.GetSessionProgressContent(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return NewSnapshot("", ""), nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	runID, err := queries.GetSessionProgressRunID(ctx, key)
	if err != nil {
		return Snapshot{}, err
	}
	return NewSnapshot(runID, content), nil
}

func (s *SQLStore) Snapshot(ctx context.Context, sessionID string) (Snapshot, error) {
	var snapshot Snapshot
	err := db.InTx(ctx, s.database, s.queries, nil, func(q *db.Queries, _ *sql.Tx) error {
		var err error
		snapshot, err = querySnapshot(ctx, q, normalizeKey(sessionID))
		return err
	})
	return snapshot, err
}

func (s *SQLStore) Update(ctx context.Context, sessionID string, change func(Snapshot) (string, error)) (Snapshot, error) {
	key := normalizeKey(sessionID)
	if key == "" {
		return Snapshot{}, fmt.Errorf("session id required")
	}
	var snapshot Snapshot
	err := db.InTx(ctx, s.database, s.queries, nil, func(q *db.Queries, _ *sql.Tx) error {
		var err error
		snapshot, err = querySnapshot(ctx, q, key)
		if err != nil {
			return err
		}
		content, err := change(snapshot)
		if err != nil {
			return err
		}
		content = clampContent(content)
		if content == snapshot.Content {
			return nil
		}
		if err := q.UpsertSessionProgressContent(ctx, db.UpsertSessionProgressContentParams{SessionID: key, WorkflowRunID: snapshot.RunID, Content: content, UpdatedAt: db.FormatTime(time.Now().UTC())}); err != nil {
			return err
		}
		snapshot = NewSnapshot(snapshot.RunID, content)
		return nil
	})
	return snapshot, err
}
