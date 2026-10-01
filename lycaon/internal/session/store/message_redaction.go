package store

import (
	"context"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/pkg/api"
)

// MessageRedactor screens one transcript row for storage.
type MessageRedactor func(ctx context.Context, msg api.Message) (api.Message, bool)

// messageRedactor applies one runtime policy across store handles.
var messageRedactor atomic.Pointer[MessageRedactor]

// SetMessageRedactor installs transcript screening at the store boundary.
func SetMessageRedactor(fn MessageRedactor) {
	if fn == nil {
		messageRedactor.Store(nil)
		return
	}
	messageRedactor.Store(&fn)
}

// screenMessageForStore applies the configured policy to one row.
func screenMessageForStore(ctx context.Context, msg api.Message) api.Message {
	loaded := messageRedactor.Load()
	if loaded == nil {
		return msg
	}
	screened, _ := (*loaded)(ctx, msg)
	return screened
}

// screenMessagesForStore screens a batch in place for later publication.
func screenMessagesForStore(ctx context.Context, msgs []api.Message) {
	if messageRedactor.Load() == nil {
		return
	}
	for i := range msgs {
		msgs[i] = screenMessageForStore(ctx, msgs[i])
	}
}

func messageScreenContext(ctx context.Context, projectID, sessionID string) context.Context {
	attr := secretmatch.AskAttributionFrom(ctx)
	attr.ProjectID = projectID
	attr.SessionID = sessionID
	return secretmatch.WithAskAttribution(ctx, attr)
}
