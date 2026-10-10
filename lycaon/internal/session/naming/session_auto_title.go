package naming

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/events"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) SessionFromPrompt(ctx context.Context, sess *wire.Session, promptText string) {
	if m == nil || m.store == nil || sess == nil {
		return
	}
	text := strings.TrimSpace(promptText)
	if text == "" || sess.IsWorkerChild() {
		return
	}
	projectDir := m.roots.SettingsPath(ctx, sess)
	title := NameSession(ctx, m.namer(sess, "session_title", projectDir), text)
	if title == "" {
		return
	}
	updated, err := m.store.UpdateTitleIfUnset(ctx, sess.ID, title)
	if err != nil || !updated {
		return
	}
	m.PublishSession(ctx, sess, title)
}

func (m *Service) PublishSession(ctx context.Context, sess *wire.Session, title string) {
	if m == nil || sess == nil {
		return
	}
	// Titles publish outside the lifecycle observer, so presence hears of them here.
	if m.presence != nil {
		m.presence.ObserveSession(ctx, wire.SessionEvent{ID: sess.ID, ProjectID: sess.ProjectID, Title: title})
	}
	if m.publisher == nil || m.publisher.Hub == nil {
		return
	}
	if m.store.MutationEventsOutboxed() {
		return
	}
	status := sess.Status
	// Use the stored status when the turn snapshot is stale.
	if m.store != nil {
		if fresh, err := m.store.Get(ctx, sess.ID); err == nil && fresh != nil {
			status = fresh.Status
		}
	}
	ev := wire.SessionEvent{
		ID:               sess.ID,
		ProjectID:        sess.ProjectID,
		Action:           wire.SessionEventActionUpdated,
		Title:            title,
		Status:           status,
		LastMessage:      m.lastMessage(ctx, sess.ID),
		UntrustedContent: m.store.SessionUntrustedContent(sess.ID),
	}
	key := events.PublishKey{Project: strings.TrimSpace(sess.ProjectID)}
	_ = m.publisher.Hub.Publish(ctx, wire.EventTopicSession, key, ev)
}
