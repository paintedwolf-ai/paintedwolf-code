package property

import (
	"context"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
)

func failErr(t *rapid.T, step string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
}

func mustFindMessage(t *rapid.T, ctx context.Context, store session.Store, sessionID, id string) api.Message {
	t.Helper()
	msgs, err := store.GetMessages(ctx, sessionID)
	failErr(t, "get messages", err)
	for _, m := range msgs {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("message %s not found in session %s", id, sessionID)
	return api.Message{}
}
