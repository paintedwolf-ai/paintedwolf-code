package hostctx

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

type roundModeKey struct{}

type roundMode struct {
	id   string
	mode api.SessionMode
}

// WithRoundMode binds the admitted mode to the round using it.
func WithRoundMode(ctx context.Context, sessionID string, mode api.SessionMode) context.Context {
	return context.WithValue(ctx, roundModeKey{}, roundMode{id: sessionID, mode: mode})
}

func RoundMode(ctx context.Context, sessionID string) (api.SessionMode, bool) {
	value, ok := ctx.Value(roundModeKey{}).(roundMode)
	return value.mode, ok && value.id == sessionID
}
