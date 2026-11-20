package execution

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

// Status publishes session transitions from bounded persisted previews.
type Status struct {
	sessionStore    Sessions
	transcriptStore Transcript
	events          *events.Publisher
}

func NewStatus(sessionStore Sessions, transcriptStore Transcript) *Status {
	return &Status{sessionStore: sessionStore, transcriptStore: transcriptStore}
}
func (m *Status) SetPublisher(events *events.Publisher) { m.events = events }
func (m *Status) PublishBusy(ctx context.Context, sess *api.Session, preview string, hostTurn, turnWasIdle bool) {
	if m == nil || m.events == nil || sess == nil || (hostTurn && !turnWasIdle) {
		return
	}
	if preview == "" {
		preview = LastAssistantMessageContent(ctx, m.transcriptStore, sess.ID)
	}
	m.events.PublishSession(ctx, sess.ProjectID, sess.ID, api.SessionStatusBusy, preview)
}

func (m *Status) PublishIdle(ctx context.Context, sessionID string, disposition api.SessionIdleDisposition) {
	m.PublishIdleWithMessage(ctx, sessionID, disposition, "")
}

func (m *Status) PublishIdleWithMessage(ctx context.Context, sessionID string, disposition api.SessionIdleDisposition, messageOverride string) {
	if m == nil || m.events == nil || (m.sessionStore == nil || m.transcriptStore == nil) {
		return
	}
	updated, err := m.sessionStore.Get(ctx, sessionID)
	if err != nil {
		return
	}
	lastMessage := strings.TrimSpace(messageOverride)
	if lastMessage == "" {
		lastMessage = LastAssistantMessageContent(ctx, m.transcriptStore, sessionID)
	}
	m.events.PublishSessionIdle(ctx, updated.ProjectID, sessionID, lastMessage, disposition)
}

// LastAssistantMessageContent is the idle preview: one tail read, never the
// transcript. A read failure yields an empty preview rather than a lost event.

func LastAssistantMessageContent(ctx context.Context, store Transcript, sessionID string) string {
	if store == nil {
		return ""
	}
	content, err := store.LastAssistantMessageContent(ctx, sessionID)
	if err != nil {
		return ""
	}
	return content
}

// Preparing publishes an idempotent activity lease.
func (m *Status) Preparing(ctx context.Context, sess *api.Session, sessionID string) func() {
	if m == nil || m.events == nil || sess == nil || strings.TrimSpace(sessionID) == "" {
		return func() {}
	}
	event := api.ActivityEvent{
		ActivityID: uuid.NewString(),
		SessionID:  strings.TrimSpace(sessionID),
		Kind:       api.ActivityKindPreparingContext,
		Status:     api.ActivityStatusActive,
		StartedAt:  time.Now().UTC(),
	}
	m.events.PublishActivity(ctx, sess.ProjectID, event.SessionID, event)
	var once sync.Once
	return func() {
		once.Do(func() {
			event.Status = api.ActivityStatusDone
			m.events.PublishActivity(context.WithoutCancel(ctx), sess.ProjectID, event.SessionID, event)
		})
	}
}

func (m *Status) PublishAssistant(ctx context.Context, sessionID, content string) {
	if m == nil || m.events == nil || (m.sessionStore == nil || m.transcriptStore == nil) {
		return
	}
	if sess, err := m.sessionStore.Get(ctx, sessionID); err == nil && sess != nil {
		m.events.PublishSession(ctx, sess.ProjectID, sessionID, sess.Status, content)
	}
}
