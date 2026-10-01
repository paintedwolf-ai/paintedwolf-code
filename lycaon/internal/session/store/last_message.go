package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lycaon/lycaon/pkg/api"
)

// LastAssistantMessageContent returns the newest assistant message body, or
// "" when the session has none. It reads one row from the transcript tail
// rather than the whole history.
func (s *SQL) LastAssistantMessageContent(ctx context.Context, sessionID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	content, err := s.queries.LastAssistantMessageContent(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return content, err
}

// LastTurnMessageContent returns the newest user or assistant message body,
// or "" when the session has neither.
func (s *SQL) LastTurnMessageContent(ctx context.Context, sessionID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	content, err := s.queries.LastTurnMessageContent(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return content, err
}

// LastAssistantMessageContent scans the in-memory transcript from its tail.
func (s *Memory) LastAssistantMessageContent(ctx context.Context, sessionID string) (string, error) {
	return s.lastContentByRole(ctx, sessionID, api.MessageRoleAssistant)
}

// LastTurnMessageContent scans the in-memory transcript from its tail.
func (s *Memory) LastTurnMessageContent(ctx context.Context, sessionID string) (string, error) {
	return s.lastContentByRole(ctx, sessionID, api.MessageRoleUser, api.MessageRoleAssistant)
}

func (s *Memory) lastContentByRole(ctx context.Context, sessionID string, roles ...api.MessageRole) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	msgs := s.messages[sessionID]
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, role := range roles {
			if msgs[i].Role == role {
				return msgs[i].Content, nil
			}
		}
	}
	return "", nil
}
