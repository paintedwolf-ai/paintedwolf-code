package property

import (
	"context"

	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
)

func failErr(t *rapid.T, step string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
}

func mustFindMessage(t *rapid.T, ctx context.Context, store *sessionstore.Repositories, sessionID, id string) api.Message {
	t.Helper()
	msgs, err := store.Transcript.GetMessages(ctx, sessionID)
	failErr(t, "get messages", err)
	for _, m := range msgs {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("message %s not found in session %s", id, sessionID)
	return api.Message{}
}
