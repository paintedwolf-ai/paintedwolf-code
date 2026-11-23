package progress

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

func querySnapshot(ctx context.Context, queries *db.Queries, key string) (Snapshot, error) {
	row, err := queries.GetSessionProgressSnapshot(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshotVersion("", "", nil, "", 0), nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	var itemIDs []string
	if row.ItemIdsJson != "" {
		_ = json.Unmarshal([]byte(row.ItemIdsJson), &itemIDs)
	}
	return snapshotVersion(row.WorkflowRunID, row.Content, itemIDs, row.UpdatedAt, uint64(row.DocumentVersion)), nil //nolint:gosec // G115 — document versions start at zero and only increase.
}

func (s *SQLStore) Snapshot(ctx context.Context, sessionID string) (Snapshot, error) {
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
