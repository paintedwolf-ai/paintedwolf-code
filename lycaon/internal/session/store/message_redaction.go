package store

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/pkg/api"
)

// MessageRedactor screens one transcript row for storage.
type MessageRedactor func(ctx context.Context, msg api.Message) (api.Message, bool)

type messageRedactorRegistration struct{ fn MessageRedactor }

var messageRedactorMu sync.Mutex
var activeMessageRedactor *messageRedactorRegistration

// SetMessageRedactor installs transcript screening until its owner releases it.
func SetMessageRedactor(fn MessageRedactor) func() {
	registration := &messageRedactorRegistration{fn: fn}
	messageRedactorMu.Lock()
	activeMessageRedactor = registration
	messageRedactorMu.Unlock()
	return func() {
		messageRedactorMu.Lock()
		defer messageRedactorMu.Unlock()
		if activeMessageRedactor == registration {
			activeMessageRedactor = nil
		}
		registration.fn = nil
	}
}

// screenMessageForStore applies the configured policy to one row.
func screenMessageForStore(ctx context.Context, msg api.Message) api.Message {
	messageRedactorMu.Lock()
	var fn MessageRedactor
	if activeMessageRedactor != nil {
		fn = activeMessageRedactor.fn
	}
	messageRedactorMu.Unlock()
	if fn == nil {
		return msg
	}
	screened, _ := fn(ctx, msg)
	return screened
}

// screenMessagesForStore screens a batch in place for later publication.
func screenMessagesForStore(ctx context.Context, msgs []api.Message) {
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
