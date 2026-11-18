package store

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/timelayout"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/pkg/api"
)

// AppendDraftVersion snapshots one superseded coordinator draft attempt.
func (s *sqlCompaction) AppendDraftVersion(ctx context.Context, sessionID, slotID, body, outcomeCode string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	slotID = strings.TrimSpace(slotID)
	if sessionID == "" || slotID == "" {
		return 0, fmt.Errorf("session_id and slot_id required")
	}
	tx, err := s.core.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.core.queries.WithTx(tx)
	nextIndex, err := qtx.NextDraftVersionIndex(ctx, db.NextDraftVersionIndexParams{
		SessionID: sessionID,
		SlotID:    slotID,
	})
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	if err := qtx.InsertDraftVersion(ctx, db.InsertDraftVersionParams{
		SessionID:    sessionID,
		SlotID:       slotID,
		VersionIndex: nextIndex,
		Body:         body,
		OutcomeCode:  strings.TrimSpace(outcomeCode),
		CreatedAt:    timelayout.Format(now),
	}); err != nil {
		return 0, err
	}
	projectID, err := qtx.GetSessionProjectID(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	version := api.DraftVersion{
		VersionIndex: int(nextIndex),
		Body:         body,
		OutcomeCode:  strings.TrimSpace(outcomeCode),
		CreatedAt:    now,
	}
	if err := search.SyncDraftVersionWriteThrough(ctx, tx, projectID, sessionID, slotID, version); err != nil {
		return 0, err
	}
	count, err := qtx.CountDraftVersions(ctx, db.CountDraftVersionsParams{
		SessionID: sessionID,
		SlotID:    slotID,
	})
	if err != nil {
		return 0, err
	}
	return int(count), tx.Commit()
}

// ListDraftVersions returns sidecar snapshots for one draft slot in version order.
func (s *sqlCompaction) ListDraftVersions(ctx context.Context, sessionID, slotID string) ([]api.DraftVersion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	slotID = strings.TrimSpace(slotID)
	if sessionID == "" || slotID == "" {
		return nil, fmt.Errorf("session_id and slot_id required")
	}
	rows, err := s.core.queries.ListDraftVersions(ctx, db.ListDraftVersionsParams{
		SessionID: sessionID,
		SlotID:    slotID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]api.DraftVersion, 0, len(rows))
	for _, r := range rows {
		parsed, err := db.ParseTime(r.CreatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, api.DraftVersion{
			VersionIndex: int(r.VersionIndex),
			Body:         r.Body,
			OutcomeCode:  r.OutcomeCode,
			CreatedAt:    parsed,
		})
	}
	return out, nil
}

// CountDraftVersions returns the number of superseded snapshots for a draft slot.
func (s *sqlCompaction) CountDraftVersions(ctx context.Context, sessionID, slotID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	slotID = strings.TrimSpace(slotID)
	if sessionID == "" || slotID == "" {
		return 0, fmt.Errorf("session_id and slot_id required")
	}
	count, err := s.core.queries.CountDraftVersions(ctx, db.CountDraftVersionsParams{
		SessionID: sessionID,
		SlotID:    slotID,
	})
	return int(count), err
}

func draftVersionsKey(sessionID, slotID string) string {
	return sessionID + "\x00" + slotID
}
