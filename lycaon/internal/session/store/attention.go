package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/attention"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// ListAttentionCandidates selects unarchived root sessions with active, waiting, or unread work.
func (s *SQL) ListAttentionCandidates(ctx context.Context) ([]attention.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.queries.ListAttentionCandidateSessions(ctx)
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
func (s *SQL) LatestFinishes(ctx context.Context) (map[string]time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.queries.ListLatestFinishedTurns(ctx)
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

// LatestFinishes returns each session's newest completed turn.
func (s *Memory) LatestFinishes(ctx context.Context) (map[string]time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latestFinishesLocked(), nil
}

func (s *Memory) latestFinishesLocked() map[string]time.Time {
	out := make(map[string]time.Time)
	for _, turn := range s.turns {
		if turn.Status == TurnStatusComplete && turn.CompletedAt != nil && turn.CompletedAt.After(out[turn.SessionID]) {
			out[turn.SessionID] = *turn.CompletedAt
		}
	}
	return out
}

// ListAttentionCandidates selects unarchived root sessions that are active or
// hold an unread completed turn. Memory holds no checkpoints or workflow runs,
// so those never select an idle session here.
func (s *Memory) ListAttentionCandidates(ctx context.Context) ([]attention.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]attention.Candidate, 0)
	finishes := s.latestFinishesLocked()
	for _, sess := range s.sessions {
		if sess == nil || sess.ArchivedAt != nil || strings.TrimSpace(sess.ParentSessionID) != "" {
			continue
		}
		finished, hasFinish := finishes[sess.ID]
		unread := hasFinish && (sess.SeenAt == nil || finished.After(*sess.SeenAt))
		if sess.Status == api.SessionStatusIdle && !unread {
			continue
		}
		statusSince, changed := s.statusChangedAt[sess.ID]
		if !changed {
			statusSince = sess.CreatedAt
		}
		out = append(out, attention.Candidate{
			SessionID:   sess.ID,
			ProjectID:   sess.ProjectID,
			Title:       strings.TrimSpace(sess.Title),
			Status:      sess.Status,
			StatusSince: statusSince,
			SeenAt:      sess.SeenAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out, nil
}
