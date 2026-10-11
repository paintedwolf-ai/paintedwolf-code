package transcript

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) StampHostWaitCloseout(ctx context.Context, sessionID string, message *api.Message, turnTools []string) error {
	if message == nil {
		return nil
	}
	visibility := message.Visibility
	StampHostWaitOnlyAssistant(message, true, turnTools)
	if message.Visibility == visibility {
		return nil
	}
	return m.Update(ctx, sessionID, message.ID, *message)
}
