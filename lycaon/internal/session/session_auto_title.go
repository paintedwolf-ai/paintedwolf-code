package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/events"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (m *Manager) autoTitleSessionFromPrompt(ctx context.Context, sess *wire.Session, promptText string) {
	if m == nil || m.store == nil || sess == nil {
		return
	}
	text := strings.TrimSpace(promptText)
	if text == "" || sess.IsWorkerChild() {
		return
	}
	projectDir := m.overlayProjectDir(ctx, sess)
	title := NameSession(ctx, m.sessionNamer(sess, "session_title", projectDir), text)
	if title == "" {
		return
	}
	updated, err := m.store.UpdateTitleIfUnset(ctx, sess.ID, title)
	if err != nil || !updated {
		return
	}
	m.publishSessionTitleUpdated(ctx, sess, title)
}

func (m *Manager) publishSessionTitleUpdated(ctx context.Context, sess *wire.Session, title string) {
	if m == nil || sess == nil {
		return
	}
	// Titles publish outside the lifecycle observer, so presence hears of them here.
	m.agentPresence.ObserveSession(ctx, wire.SessionEvent{ID: sess.ID, ProjectID: sess.ProjectID, Title: title})
	if m.events == nil || m.events.Hub == nil {
		return
	}
	if m.mutationEventsOutboxed() {
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
		LastMessage:      lastUserOrAssistantMessageContent(ctx, m.store, sess.ID),
		UntrustedContent: m.store.SessionUntrustedContent(sess.ID),
	}
	key := events.PublishKey{Project: strings.TrimSpace(sess.ProjectID)}
	_ = m.events.Hub.Publish(ctx, wire.EventTopicSession, key, ev)
}
