package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/pkg/api"
)

// AppendDraftVersion snapshots one superseded coordinator draft attempt.
func (s *SQL) AppendDraftVersion(ctx context.Context, sessionID, slotID, body, outcomeCode string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	slotID = strings.TrimSpace(slotID)
	if sessionID == "" || slotID == "" {
		return 0, fmt.Errorf("session_id and slot_id required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
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
		CreatedAt:    db.FormatTime(now),
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
func (s *SQL) ListDraftVersions(ctx context.Context, sessionID, slotID string) ([]api.DraftVersion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	slotID = strings.TrimSpace(slotID)
	if sessionID == "" || slotID == "" {
		return nil, fmt.Errorf("session_id and slot_id required")
	}
	rows, err := s.queries.ListDraftVersions(ctx, db.ListDraftVersionsParams{
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
func (s *SQL) CountDraftVersions(ctx context.Context, sessionID, slotID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	slotID = strings.TrimSpace(slotID)
	if sessionID == "" || slotID == "" {
		return 0, fmt.Errorf("session_id and slot_id required")
	}
	count, err := s.queries.CountDraftVersions(ctx, db.CountDraftVersionsParams{
		SessionID: sessionID,
		SlotID:    slotID,
	})
	return int(count), err
}

func draftVersionsKey(sessionID, slotID string) string {
	return sessionID + "\x00" + slotID
}

func (s *Memory) AppendDraftVersion(ctx context.Context, sessionID, slotID, body, outcomeCode string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	slotID = strings.TrimSpace(slotID)
	if sessionID == "" || slotID == "" {
		return 0, fmt.Errorf("session_id and slot_id required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return 0, ErrSessionNotFound
	}
	key := draftVersionsKey(sessionID, slotID)
	versions := s.draftVersions[key]
	nextIndex := len(versions)
	versions = append(versions, api.DraftVersion{
		VersionIndex: nextIndex,
		Body:         body,
		OutcomeCode:  strings.TrimSpace(outcomeCode),
		CreatedAt:    time.Now().UTC(),
	})
	s.draftVersions[key] = versions
	return len(versions), nil
}

func (s *Memory) ListDraftVersions(ctx context.Context, sessionID, slotID string) ([]api.DraftVersion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	slotID = strings.TrimSpace(slotID)
	if sessionID == "" || slotID == "" {
		return nil, fmt.Errorf("session_id and slot_id required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return nil, ErrSessionNotFound
	}
	key := draftVersionsKey(sessionID, slotID)
	versions := s.draftVersions[key]
	out := make([]api.DraftVersion, len(versions))
	copy(out, versions)
	return out, nil
}

func (s *Memory) CountDraftVersions(ctx context.Context, sessionID, slotID string) (int, error) {
	versions, err := s.ListDraftVersions(ctx, sessionID, slotID)
	if err != nil {
		return 0, err
	}
	return len(versions), nil
}
