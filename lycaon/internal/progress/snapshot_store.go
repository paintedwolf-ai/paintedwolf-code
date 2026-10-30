package progress

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
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
	return snapshotVersion(record.runID, record.content, strconv.FormatUint(record.revision, 10)), nil
}

func (s *MemoryStore) Update(ctx context.Context, sessionID string, change func(Snapshot) (string, error)) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := normalizeKey(sessionID)
	record := s.bySess[key]
	current := snapshotVersion(record.runID, record.content, strconv.FormatUint(record.revision, 10))
	content, err := change(current)
	if err != nil {
		return current, err
	}
	if record.content != clampContent(content) {
		record.revision++
	}
	record.content = clampContent(content)
	s.bySess[key] = record
	return snapshotVersion(record.runID, record.content, strconv.FormatUint(record.revision, 10)), nil
}

func querySnapshot(ctx context.Context, queries *db.Queries, key string) (Snapshot, error) {
	row, err := queries.GetSessionProgressSnapshot(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshotVersion("", "", ""), nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	return snapshotVersion(row.WorkflowRunID, row.Content, row.UpdatedAt), nil
}

func (s *SQLStore) Snapshot(ctx context.Context, sessionID string) (Snapshot, error) {
	if s == nil || s.queries == nil {
		return Snapshot{}, fmt.Errorf("progress store not configured")
	}
	return querySnapshot(ctx, s.queries, normalizeKey(sessionID))
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
		stamp := db.FormatTime(nextSnapshotTime(snapshot.version))
		if err := q.UpsertSessionProgressContent(ctx, db.UpsertSessionProgressContentParams{SessionID: key, WorkflowRunID: snapshot.RunID, Content: content, UpdatedAt: stamp}); err != nil {
			return err
		}
		snapshot = snapshotVersion(snapshot.RunID, content, stamp)
		return nil
	})
	return snapshot, err
}

func nextSnapshotTime(previous string) time.Time {
	now := time.Now().UTC()
	if before, err := time.Parse(time.RFC3339Nano, previous); err == nil && !now.After(before) {
		return before.Add(time.Nanosecond)
	}
	return now
}
