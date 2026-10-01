package session

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

// AppendAndPublishMessages stamps and persists messages, then publishes their append events.
func (m *Manager) AppendAndPublishMessages(ctx context.Context, sessionID string, msgs ...api.Message) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("session store not configured")
	}
	if len(msgs) == 0 {
		return nil
	}
	stamped, err := m.prepareAppendForStore(ctx, sessionID, msgs)
	if err != nil {
		return err
	}
	if err := m.store.AppendMessages(ctx, sessionID, stamped...); err != nil {
		return err
	}
	m.publishMessageAppends(ctx, sessionID, stamped...)
	return nil
}

func (m *Manager) stampMessagesForStore(msgs []api.Message) {
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

func (m *Manager) publishMessageAppends(ctx context.Context, sessionID string, msgs ...api.Message) {
	if m == nil || m.events == nil || len(msgs) == 0 {
		return
	}
	if m.mutationEventsOutboxed() {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return
	}
	project := sessionProjectKey(sess)
	for _, msg := range msgs {
		if msg.ID == "" {
			continue
		}
		stamped := m.Streams().StampMessage(sessionID, msg)
		m.events.PublishMessageAppend(ctx, project, sessionID, stamped)
	}
}

func (m *Manager) publishMessagePatch(ctx context.Context, sessionID string, msg api.Message) {
	if m == nil || m.events == nil || msg.ID == "" {
		return
	}
	if m.mutationEventsOutboxed() {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return
	}
	m.events.PublishMessagePatch(ctx, sessionProjectKey(sess), sessionID, m.Streams().StampMessage(sessionID, msg))
}

func (m *Manager) mutationEventsOutboxed() bool {
	if m == nil || m.store == nil {
		return false
	}
	return m.store.MutationEventsOutboxed()
}
