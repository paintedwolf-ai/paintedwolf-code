package progress

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/timelayout"
	"slices"
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

func (s *SQLStore) Update(ctx context.Context, sessionID string, change func(Snapshot) (Document, error)) (Snapshot, error) {
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
		doc, err := change(snapshot)
		if err != nil {
			return err
		}
		content := clampContent(doc.Content)
		nextIDs := resolveDocumentItemIDs(snapshot, Document{Content: content, ItemIDs: doc.ItemIDs})
		if content == snapshot.Content && slices.Equal(nextIDs, snapshot.ItemIDs) {
			return nil
		}
		itemIDsJSON, err := json.Marshal(nextIDs)
		if err != nil {
			return err
		}
		stamp := timelayout.Format(nextSnapshotTime(snapshot.version))
		if err := q.UpsertSessionProgressContent(ctx, db.UpsertSessionProgressContentParams{
			SessionID:     key,
			WorkflowRunID: snapshot.RunID,
			Content:       content,
			UpdatedAt:     stamp,
			ItemIdsJson:   string(itemIDsJSON),
		}); err != nil {
			return err
		}
		nextVer := snapshot.documentVersion + 1
		if nextVer == 0 {
			nextVer = 1
		}
		snapshot = snapshotVersion(snapshot.RunID, content, nextIDs, stamp, nextVer)
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
