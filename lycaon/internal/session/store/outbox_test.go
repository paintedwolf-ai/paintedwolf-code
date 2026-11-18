package store

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSQLMutationOutboxCommitsObserverEvents(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	hub := events.NewMemoryHub()
	ch, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: testdbseed.DefaultProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe events", err)
	defer unsubscribe()
	outbox := eventoutbox.New(database, hub)
	outbox.Start(t.Context())
	defer outbox.Close()
	store := NewSQL(database)
	store.SetEventOutbox(outbox)

	sess, err := store.Sessions.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	waitOutboxEmpty(t, database)
	hub.FlushDebounced()
	created := testutil.Receive(t, "session-created observer event", ch)
	if created.Topic != api.EventTopicSession {
		t.Fatalf("created topic = %q", created.Topic)
	}

	message := api.Message{Role: api.MessageRoleAssistant, Content: "private tool-step explanation", ToolCalls: []api.ToolCall{{
		Name: "terminal_send", Args: map[string]any{"input": "secret", "id": "pty"},
	}}}
	testutil.FailErr(t, "append message", store.Transcript.AppendMessages(t.Context(), sess.ID, message))
	waitOutboxEmpty(t, database)
	envelope := testutil.Receive(t, "message observer event", ch)
	var event api.MessageEvent
	testutil.FailErr(t, "decode message event", json.Unmarshal(envelope.Data, &event))
	if event.Message.Content != "" {
		t.Fatalf("observer exposed tool-step prose: %q", event.Message.Content)
	}
	if event.Message.ToolCalls[0].Args["input"] != messageview.RedactedToolArgPlaceholder {
		t.Fatalf("observer input = %v", event.Message.ToolCalls[0].Args["input"])
	}
	stored, err := store.Transcript.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read messages", err)
	if stored[0].Content != message.Content {
		t.Fatal("observer projection changed recorded tool-step prose")
	}
	if stored[0].ToolCalls[0].Args["input"] != "secret" {
		t.Fatalf("stored input = %v", stored[0].ToolCalls[0].Args["input"])
	}
}

func waitOutboxEmpty(t *testing.T, database db.Handle) {
	t.Helper()
	testutil.WaitFor(t, "the event outbox drains", func() bool {
		var count int
		err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM event_outbox").Scan(&count)
		return err == nil && count == 0
	})
}
