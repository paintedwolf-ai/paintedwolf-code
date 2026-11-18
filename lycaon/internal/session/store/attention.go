package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/attention"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// ListAttentionCandidates selects unarchived root sessions with active, waiting, or unread work.
func (s *sqlSessionViews) ListAttentionCandidates(ctx context.Context) ([]attention.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.core.queries.ListAttentionCandidateSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list attention candidates: %w", err)
	}
	out := make([]attention.Candidate, 0, len(rows))
	for _, row := range rows {
		statusSince, err := db.ParseTime(row.StatusSince)
		if err != nil {
			return nil, fmt.Errorf("attention candidate %s status_since: %w", row.ID, err)
		}
		seenAt, err := db.TimePtrFromNull(row.SeenAt)
		if err != nil {
			return nil, fmt.Errorf("attention candidate %s seen_at: %w", row.ID, err)
		}
		out = append(out, attention.Candidate{
			SessionID:   row.ID,
			ProjectID:   row.ProjectID,
			Title:       strings.TrimSpace(row.Title.String),
			Status:      api.SessionStatus(row.Status),
			StatusSince: statusSince,
			SeenAt:      seenAt,
		})
	}
	return out, nil
}

// LatestFinishes returns each session's newest completed turn. Sessions that
// have never completed one are absent.
func (s *sqlSessionViews) LatestFinishes(ctx context.Context) (map[string]time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.core.queries.ListLatestFinishedTurns(ctx)
	if err != nil {
		return nil, fmt.Errorf("list latest finished turns: %w", err)
	}
	out := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		at, err := db.ParseTime(row.LastFinishedAt)
		if err != nil {
			return nil, fmt.Errorf("latest finished turn for %s: %w", row.SessionID, err)
		}
		out[row.SessionID] = at
	}
	return out, nil
}
