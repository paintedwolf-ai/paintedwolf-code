package store

import (
	"context"
	"database/sql"
	"errors"
)

// LastAssistantMessageContent returns the newest transcript-visible assistant
// message body, or "" when there is none.
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

// LastTurnMessageContent returns the newest transcript-visible user or
// assistant message body, or "" when there is none.
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
