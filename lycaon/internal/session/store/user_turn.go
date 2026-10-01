package store

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// UserTurnOrdinal returns the newest live opener's permanent source identity.
func (s *Memory) UserTurnOrdinal(ctx context.Context, sessionID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentUserTurnLocked(sessionID), nil
}

func (s *SQL) UserTurnOrdinal(ctx context.Context, sessionID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	n, err := s.queries.GetSessionUserTurnOrdinal(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func (s *Memory) currentUserTurnLocked(sessionID string) int {
	for i := len(s.messages[sessionID]) - 1; i >= 0; i-- {
		if n := s.userTurns[sessionID][s.messages[sessionID][i].ID]; n > 0 {
			return n
		}
	}
	return 0
}

// UserIntentBefore reads the latest human opener at a worker's creation boundary.
func (s *SQL) UserIntentBefore(ctx context.Context, sessionID string, before time.Time) (time.Time, error) {
	value, err := s.queries.UserIntentBefore(ctx, db.UserIntentBeforeParams{SessionID: sessionID, Ts: db.FormatTime(before)})
	if db.IsNoRows(err) {
		return before, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return db.ParseTime(value)
}

func (s *Memory) UserIntentBefore(ctx context.Context, sessionID string, before time.Time) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	messages := s.messages[sessionID]
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if api.IsUserIntentMessage(msg) && msg.Origin == api.MessageOriginUser && !msg.CreatedAt.After(before) {
			return msg.CreatedAt, nil
		}
	}
	return before, nil
}
