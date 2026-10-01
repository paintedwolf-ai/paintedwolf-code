package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

// Appends and updates apply the same durable secret policy.
func (m *Manager) screenForStore(ctx context.Context, msg api.Message) api.Message {
	if m == nil || m.redactMessageForStorage == nil {
		return msg
	}
	out, _ := m.redactMessageForStorage(ctx, msg)
	return out
}

// screenMessagesForStore screens a batch in place before it is written.
func (m *Manager) screenMessagesForStore(ctx context.Context, msgs []api.Message) {
	if m == nil || m.redactMessageForStorage == nil {
		return
	}
	for i := range msgs {
		msgs[i] = m.screenForStore(ctx, msgs[i])
	}
}

// Detach and screen the batch before storage, indexing, and publication.
func (m *Manager) prepareAppendForStore(ctx context.Context, sessionID string, msgs []api.Message) ([]api.Message, error) {
	stamped := append([]api.Message(nil), msgs...)
	if err := workercontext.Stamp(ctx, stamped); err != nil {
		return nil, err
	}
	m.stampMessagesForStore(stamped)
	m.attachMessageNavigationRefs(ctx, sessionID, stamped)
	m.screenMessagesForStore(m.messageSecretContext(ctx, sessionID), stamped)
	return stamped, nil
}

// Evidence and receipt text use the transcript’s durable secret policy.
func (m *Manager) screenTextForStore(ctx context.Context, text string) string {
	if m == nil || m.redactMessageForStorage == nil || text == "" {
		return text
	}
	screened, _ := m.redactMessageForStorage(ctx, api.Message{Content: text})
	return screened.Content
}

func (m *Manager) messageSecretContext(ctx context.Context, sessionID string) context.Context {
	if sess, err := m.store.Get(ctx, sessionID); err == nil {
		attr := secretmatch.AskAttributionFrom(ctx)
		attr.ProjectID = sess.ProjectID
		attr.SessionID = sessionID
		ctx = secretmatch.WithAskAttribution(ctx, attr)
	}
	return ctx
}
