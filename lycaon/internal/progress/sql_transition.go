package progress

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/timelayout"
	api "github.com/lycaon/lycaon/pkg/api"
)

func (s *SQLStore) Update(ctx context.Context, sessionID string, change func(Snapshot) (Document, error)) (Snapshot, error) {
	key := normalizeKey(sessionID)
	if key == "" {
		return Snapshot{}, fmt.Errorf("session id required")
	}
	if s == nil || s.transcript == nil {
		return Snapshot{}, fmt.Errorf("progress transactions not configured")
	}
	var before, after Snapshot
	err := s.transcript.AppendMessagesWith(ctx, key, func(tx *sql.Tx) ([]api.Message, error) {
		q := db.New(tx)
		var err error
		before, err = querySnapshot(ctx, q, key)
		after = before
		if err != nil {
			return nil, err
		}
		doc, err := change(before)
		if err != nil {
			return nil, err
		}
		after, err = writeTransition(ctx, q, key, before, doc)
		if err != nil {
			return nil, err
		}
		return transitionMessages(before, after), nil
	})
	if err != nil {
		return before, err
	}
	if !before.Equal(after) {
		NotifyWriteObservers(ctx, WriteEvent{SessionID: key, Before: before, After: after})
	}
	return after, nil
}

func writeTransition(ctx context.Context, q *db.Queries, key string, before Snapshot, doc Document) (Snapshot, error) {
	content := clampContent(doc.Content)
	ids := resolveDocumentItemIDs(before, Document{Content: content, ItemIDs: doc.ItemIDs})
	runID := strings.TrimSpace(doc.RunID)
	if runID == "" {
		runID = before.RunID
	}
	if content == before.Content && slices.Equal(ids, before.ItemIDs) && runID == before.RunID {
		return before, nil
	}
	if runID == "" {
		run, err := q.ActiveWorkflowRunBySession(ctx, key)
		if err != nil {
			return Snapshot{}, fmt.Errorf("resolve progress workflow run: %w", err)
		}
		runID = run.ID
	}
	run, err := q.GetWorkflowRun(ctx, runID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read progress workflow run: %w", err)
	}
	if run.SessionID != key {
		return Snapshot{}, fmt.Errorf("progress workflow run belongs to another session")
	}
	if ids == nil {
		ids = []string{}
	}
	rawIDs, err := json.Marshal(ids)
	if err != nil {
		return Snapshot{}, err
	}
	stamp := timelayout.Format(nextSnapshotTime(before.version))
	if err := q.UpsertSessionProgressContent(ctx, db.UpsertSessionProgressContentParams{
		SessionID: key, WorkflowRunID: runID, Content: content, UpdatedAt: stamp, ItemIdsJson: string(rawIDs),
	}); err != nil {
		return Snapshot{}, err
	}
	return querySnapshot(ctx, q, key)
}
