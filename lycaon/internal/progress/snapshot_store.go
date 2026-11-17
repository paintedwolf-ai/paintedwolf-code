package progress

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	return snapshotVersion(record.runID, record.content, record.itemIDs, strconv.FormatUint(record.revision, 10), record.revision), nil
}

func (s *MemoryStore) Update(ctx context.Context, sessionID string, change func(Snapshot) (Document, error)) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	before, after, err := s.update(ctx, sessionID, change)
	s.mu.Unlock()
	if err == nil && !before.Equal(after) {
		NotifyWriteObservers(ctx, WriteEvent{SessionID: sessionID, Before: before, After: after})
	}
	return after, err
}

func (s *MemoryStore) update(ctx context.Context, sessionID string, change func(Snapshot) (Document, error)) (Snapshot, Snapshot, error) {
	key := normalizeKey(sessionID)
	record := s.bySess[key]
	before := snapshotVersion(record.runID, record.content, record.itemIDs, strconv.FormatUint(record.revision, 10), record.revision)
	if err := ctx.Err(); err != nil {
		return before, before, err
	}
	doc, err := change(before)
	if err != nil {
		return before, before, err
	}
	if err := ctx.Err(); err != nil {
		return before, before, err
	}
	content := clampContent(doc.Content)
	ids := resolveDocumentItemIDs(before, Document{Content: content, ItemIDs: doc.ItemIDs})
	runID := normalizeKey(doc.RunID)
	if runID == "" {
		runID = before.RunID
	}
	if record.content != content || !slices.Equal(record.itemIDs, ids) || record.runID != runID {
		record.revision++
	}
	record.content, record.itemIDs, record.runID = content, ids, runID
	s.bySess[key] = record
	after := snapshotVersion(runID, content, ids, strconv.FormatUint(record.revision, 10), record.revision)
	return before, after, nil
}

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
