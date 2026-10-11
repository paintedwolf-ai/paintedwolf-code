package transcript

import (
	"context"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

// Appends and updates apply the same durable secret policy.
func (m *Service) Screen(ctx context.Context, msg api.Message) api.Message {
	if m == nil || m.redactMessageForStorage == nil {
		return msg
	}
	out, _ := m.redactMessageForStorage(ctx, msg)
	return out
}

// screenBatch screens a batch in place before it is written.
func (m *Service) screenBatch(ctx context.Context, msgs []api.Message) {
	if m == nil || m.redactMessageForStorage == nil {
		return
	}
	for i := range msgs {
		msgs[i] = m.Screen(ctx, msgs[i])
	}
}

// Detach and screen the batch before storage, indexing, and publication.
func (m *Service) Prepare(ctx context.Context, sessionID string, msgs []api.Message) ([]api.Message, error) {
	stamped := append([]api.Message(nil), msgs...)
	if err := workercontext.Stamp(ctx, stamped); err != nil {
		return nil, err
	}
	m.stamp(stamped)
	m.NavigationRefs(ctx, sessionID, stamped)
	m.screenBatch(m.SecretContext(ctx, sessionID), stamped)
	return stamped, nil
}

// Evidence and receipt text use the transcript’s durable secret policy.
func (m *Service) ScreenText(ctx context.Context, text string) string {
	if m == nil || m.redactMessageForStorage == nil || text == "" {
		return text
	}
	screened, _ := m.redactMessageForStorage(ctx, api.Message{Content: text})
	return screened.Content
}

func (m *Service) SecretContext(ctx context.Context, sessionID string) context.Context {
	if sess, err := m.store.Get(ctx, sessionID); err == nil {
		attr := secretmatch.AskAttributionFrom(ctx)
		attr.ProjectID = sess.ProjectID
		attr.SessionID = sessionID
		ctx = secretmatch.WithAskAttribution(ctx, attr)
	}
	return ctx
}
