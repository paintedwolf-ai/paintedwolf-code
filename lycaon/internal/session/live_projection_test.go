package session

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLiveProjectionCoalescesAndSkipsOutbox(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "live-projection.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())

	hub := events.NewMemoryHub()
	outbox := eventoutbox.New(sqlDB, hub)
	outbox.Start(t.Context())
	t.Cleanup(func() { _ = outbox.Close() })

	st := store.NewSQL(sqlDB)
	st.SetEventOutbox(outbox)
	pub := &events.Publisher{Hub: hub}
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetEventPublisher(pub)

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	waitOutboxEmpty(t, sqlDB)

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	t.Cleanup(unsub)

	msgID := "live-draft-1"
	testutil.FailErr(t, "append placeholder", st.AppendMessages(ctx, sess.ID, api.Message{
		ID:          msgID,
		Role:        api.MessageRoleAssistant,
		DraftStatus: api.DraftStatusLive,
		Visibility:  api.MessageVisibilityInternal,
		CreatedAt:   time.Now().UTC(),
	}))
	waitOutboxEmpty(t, sqlDB)
	drainMessageEvents(t, ch, 200*time.Millisecond)

	for i := 1; i <= 20; i++ {
		testutil.FailErr(t, "project live", mgr.Streams().Project(ctx, sess.ID, api.Message{
			ID:      msgID,
			Role:    api.MessageRoleAssistant,
			Content: "token-body-" + strconv.Itoa(i),
		}))
	}
	mgr.Streams().Flush(ctx, sess.ID)

	patches := collectMessageOps(t, ch, 200*time.Millisecond)
	if n := patches[api.MessageChangePatch]; n != 0 {
		t.Fatalf("live SSE patches on project hub = %d want 0", n)
	}
	if n := patches[api.MessageChangeAppend]; n != 0 {
		t.Fatalf("unexpected appends after placeholder drain: %d", n)
	}

	stored, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(stored) != 1 || stored[0].Content != "token-body-20" {
		t.Fatalf("stored live content = %q want last token body", contentOrEmpty(stored))
	}
	if stored[0].Seq != 1 {
		t.Fatalf("live persist advanced seq = %d want 1 (append seq)", stored[0].Seq)
	}

	var outboxRows int
	testutil.FailErr(t, "count outbox", sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM event_outbox`).Scan(&outboxRows))
	if outboxRows != 0 {
		t.Fatalf("outbox rows after live projection = %d want 0", outboxRows)
	}

	testutil.FailErr(t, "settle", mgr.updateMessage(ctx, sess.ID, msgID, api.Message{
		ID:         msgID,
		Role:       api.MessageRoleAssistant,
		Content:    "token-body-20",
		Visibility: api.MessageVisibilityTranscript,
	}))
	waitOutboxEmpty(t, sqlDB)
	settled := collectMessageOps(t, ch, 200*time.Millisecond)
	if settled[api.MessageChangePatch] < 1 {
		t.Fatal("settle did not publish a durable patch")
	}
}

func TestLiveProjectionPersistsLongRunningToolCallsWithoutWiping(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "live-projection-calls.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())

	hub := events.NewMemoryHub()
	outbox := eventoutbox.New(sqlDB, hub)
	outbox.Start(t.Context())
	t.Cleanup(func() { _ = outbox.Close() })

	st := store.NewSQL(sqlDB)
	st.SetEventOutbox(outbox)
	pub := &events.Publisher{Hub: hub}
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetEventPublisher(pub)

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	waitOutboxEmpty(t, sqlDB)

	msgID := "live-draft-calls"
	testutil.FailErr(t, "append placeholder", st.AppendMessages(ctx, sess.ID, api.Message{
		ID:          msgID,
		Role:        api.MessageRoleAssistant,
		DraftStatus: api.DraftStatusLive,
		Visibility:  api.MessageVisibilityInternal,
		CreatedAt:   time.Now().UTC(),
	}))
	waitOutboxEmpty(t, sqlDB)

	testutil.FailErr(t, "project live tool_calls", mgr.Streams().Project(ctx, sess.ID, api.Message{
		ID:      msgID,
		Role:    api.MessageRoleAssistant,
		Content: "running",
		ToolCalls: []api.ToolCall{{
			ID:   "tc-cmd",
			Name: "command",
			Args: map[string]any{"command": "ls"},
		}},
	}))
	mgr.Streams().Flush(ctx, sess.ID)
	withCalls, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after tool_calls", err)
	if len(withCalls[0].ToolCalls) != 1 || withCalls[0].ToolCalls[0].Name != "command" {
		t.Fatalf("live tool_calls = %+v", withCalls[0].ToolCalls)
	}
	testutil.FailErr(t, "project live content only", mgr.Streams().Project(ctx, sess.ID, api.Message{
		ID:      msgID,
		Role:    api.MessageRoleAssistant,
		Content: "still running",
	}))
	mgr.Streams().Flush(ctx, sess.ID)
	kept, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after content-only", err)
	if kept[0].Content != "still running" {
		t.Fatalf("content after content-only live patch = %q", kept[0].Content)
	}
	if len(kept[0].ToolCalls) != 1 || kept[0].ToolCalls[0].Name != "command" {
		t.Fatalf("content-only live patch wiped tool_calls: %+v", kept[0].ToolCalls)
	}
}

func contentOrEmpty(msgs []api.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	return msgs[0].Content
}

func drainMessageEvents(t *testing.T, ch <-chan api.EventEnvelope, wait time.Duration) {
	t.Helper()
	_ = collectMessageOps(t, ch, wait)
}

func collectMessageOps(t *testing.T, ch <-chan api.EventEnvelope, wait time.Duration) map[api.MessageChangeOp]int {
	t.Helper()
	got := map[api.MessageChangeOp]int{}
	deadline := time.After(wait)
	for {
		select {
		case env := <-ch:
			if env.Topic != api.EventTopicMessage {
				continue
			}
			var ev api.MessageEvent
			if err := json.Unmarshal(env.Data, &ev); err != nil {
				testutil.FailErr(t, "decode message event", err)
			}
			got[ev.Op]++
		case <-deadline:
			return got
		}
	}
}

func waitOutboxEmpty(t *testing.T, database db.Handle) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM event_outbox").Scan(&count); err == nil && count == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("event outbox did not drain")
}
