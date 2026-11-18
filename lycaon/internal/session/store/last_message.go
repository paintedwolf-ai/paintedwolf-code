package store

import (
	"context"
	"database/sql"
	"errors"
)

// LastAssistantMessageContent returns the newest assistant message body, or
// "" when the session has none. It reads one row from the transcript tail
// rather than the whole history.
func (s *sqlTranscript) LastAssistantMessageContent(ctx context.Context, sessionID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	content, err := s.core.queries.LastAssistantMessageContent(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return content, err
}

// LastTurnMessageContent returns the newest user or assistant message body,
// or "" when the session has neither.
func (s *sqlTranscript) LastTurnMessageContent(ctx context.Context, sessionID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	content, err := s.core.queries.LastTurnMessageContent(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return content, err
}
