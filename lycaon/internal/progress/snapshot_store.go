package progress

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

func querySnapshot(ctx context.Context, queries *db.Queries, key string) (Snapshot, error) {
	header, err := queries.GetSessionProgressHeader(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshotFromDB("", nil, "", 0, 1), nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	items, err := queries.ListSessionProgressItems(ctx, key)
	if err != nil {
		return Snapshot{}, err
	}
	rows := make([]Row, len(items))
	for i, it := range items {
		rows[i] = Row{
			ID:    it.ItemID,
			State: it.State,
			Label: it.Label,
		}
	}
	return snapshotFromDB(
		header.WorkflowRunID,
		rows,
		header.UpdatedAt,
		uint64(header.DocumentVersion), //nolint:gosec // G115 — constrained >= 1 in SQLite.
		uint64(header.NextItemSeq),     //nolint:gosec // G115 — constrained >= 1 in SQLite.
	), nil
}

// Snapshot reads the current session progress header and ordered items.
func (s *SQLStore) Snapshot(ctx context.Context, sessionID string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if s == nil || s.queries == nil {
		return Snapshot{}, fmt.Errorf("progress store not configured")
	}
	return querySnapshot(ctx, s.queries, normalizeKey(sessionID))
}

func nextSnapshotTime(previous string) time.Time {
	now := time.Now().UTC()
	if before, err := time.Parse(time.RFC3339Nano, previous); err == nil && !now.After(before) {
		return before.Add(time.Nanosecond)
	}
	return now
}
