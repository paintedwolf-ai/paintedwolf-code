package promptloop

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

func (l *turnProjection) appendStorageSafeMessages(
	ctx context.Context,
	sessionID string,
	history []api.Message,
	stored []api.Message,
	transient []*api.Message,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	if err := l.persistStorageSafeMessages(ctx, sessionID, stored); err != nil {
		return history, err
	}
	return foldStorageSafeMessages(history, stored, transient, st), nil
}

func (l *turnProjection) persistStorageSafeMessages(ctx context.Context, sessionID string, stored []api.Message) error {
	if l == nil || l.Deps.AppendMessages == nil {
		return fmt.Errorf("append messages not configured")
	}
	return l.Deps.AppendMessages(ctx, sessionID, stored...)
}

func (l *turnProjection) appendMessage(ctx context.Context, sessionID string, msg api.Message) (api.Message, error) {
	if l == nil || l.Deps.AppendMessages == nil {
		return api.Message{}, fmt.Errorf("append messages not configured")
	}
	rows := []api.Message{msg}
	if err := l.Deps.AppendMessages(ctx, sessionID, rows...); err != nil {
		return api.Message{}, err
	}
	return rows[0], nil
}

func foldStorageSafeMessages(
	history []api.Message,
	stored []api.Message,
	transient []*api.Message,
	st *promptLoopTurnState,
) []api.Message {
	for i := range stored {
		var raw *api.Message
		if i < len(transient) {
			raw = transient[i]
		}
		promptMsg := transientMessageFromStored(stored[i], raw)
		history = append(history, promptMsg)
		if raw != nil {
			st.rememberSecretStorageOverlay(stored[i], promptMsg)
		}
	}
	return history
}
