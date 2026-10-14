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

// GetUserTurnOpener resolves a source-turn identity to its canonical message.
func (s *SQL) GetUserTurnOpener(ctx context.Context, sessionID string, turn int) (api.Message, error) {
	id, err := s.queries.GetUserTurnOpeningMessageID(ctx, db.GetUserTurnOpeningMessageIDParams{SessionID: sessionID, Turn: int64(turn)})
	if db.IsNoRows(err) {
		return api.Message{}, nil
	}
	if err != nil {
		return api.Message{}, err
	}
	return s.GetMessage(ctx, sessionID, id)
}

func (s *Memory) GetUserTurnOpener(ctx context.Context, sessionID string, turn int) (api.Message, error) {
	if err := ctx.Err(); err != nil {
		return api.Message{}, err
	}
	s.mu.RLock()
	id := ""
	for candidate, ordinal := range s.userTurns[sessionID] {
		if ordinal == turn {
			id = candidate
			break
		}
	}
	s.mu.RUnlock()
	if id == "" {
		return api.Message{}, nil
	}
	return s.GetMessage(ctx, sessionID, id)
}
