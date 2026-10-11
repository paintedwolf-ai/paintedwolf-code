package transcript

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

// AppendPlain stamps and persists messages, then publishes their append events.
func (m *Service) AppendPlain(ctx context.Context, sessionID string, msgs ...api.Message) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("session store not configured")
	}
	if len(msgs) == 0 {
		return nil
	}
	stamped, err := m.Prepare(ctx, sessionID, msgs)
	if err != nil {
		return err
	}
	if err := m.store.AppendMessages(ctx, sessionID, stamped...); err != nil {
		return err
	}
	m.PublishAppends(ctx, sessionID, stamped...)
	return nil
}

func (m *Service) stamp(msgs []api.Message) {
	now := time.Now().UTC()
	for i := range msgs {
		if msgs[i].ID == "" {
			msgs[i].ID = uuid.NewString()
		}
		if msgs[i].CreatedAt.IsZero() {
			msgs[i].CreatedAt = now
		}
	}
}

func (m *Service) PublishAppends(ctx context.Context, sessionID string, msgs ...api.Message) {
	if m == nil || m.events == nil || len(msgs) == 0 {
		return
	}
	if m.Outboxed() {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return
	}
	project := strings.TrimSpace(sess.ProjectID)
	for _, msg := range msgs {
		if msg.ID == "" {
			continue
		}
		stamped := m.Streams.StampMessage(sessionID, msg)
		m.events.PublishMessageAppend(ctx, project, sessionID, stamped)
	}
}

func (m *Service) PublishPatch(ctx context.Context, sessionID string, msg api.Message) {
	if m == nil || m.events == nil || msg.ID == "" {
		return
	}
	if m.Outboxed() {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return
	}
	m.events.PublishMessagePatch(ctx, strings.TrimSpace(sess.ProjectID), sessionID, m.Streams.StampMessage(sessionID, msg))
}

func (m *Service) Outboxed() bool {
	if m == nil || m.store == nil {
		return false
	}
	return m.store.MutationEventsOutboxed()
}
