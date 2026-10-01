package store

import (
	"context"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

func TestSQLPatchLiveProjectionPersistsContentAndToolCalls(t *testing.T) {
	database := testdbfixture.Open(t, "live-projection.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	hub := events.NewMemoryHub()
	outbox := eventoutbox.New(database, hub)
	outbox.Start(t.Context())
	t.Cleanup(func() { _ = outbox.Close() })
	st := NewSQL(database)
	st.SetEventOutbox(outbox)

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	waitOutboxEmpty(t, database)

	msg := api.Message{
		ID:          "live-1",
		Role:        api.MessageRoleAssistant,
		CreatedAt:   time.Now().UTC(),
		Visibility:  api.MessageVisibilityInternal,
		DraftStatus: api.DraftStatusLive,
	}
	testutil.FailErr(t, "append", st.AppendMessages(ctx, sess.ID, msg))
	waitOutboxEmpty(t, database)

	before, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages before", err)
	if len(before) != 1 {
		t.Fatalf("messages = %d want 1", len(before))
	}
	seq := before[0].Seq

	testutil.FailErr(t, "PatchLiveProjection content", st.PatchLiveProjection(ctx, sess.ID, msg.ID, "streaming body", nil))

	after, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after content", err)
	if after[0].Content != "streaming body" {
		t.Fatalf("content = %q", after[0].Content)
	}
	if after[0].Seq != seq {
		t.Fatalf("seq = %d want %d (live persist must not advance seq)", after[0].Seq, seq)
	}

	calls := []api.ToolCall{{ID: "tc-cmd", Name: "command", Args: map[string]any{"command": "ls"}}}
	testutil.FailErr(t, "PatchLiveProjection calls", st.PatchLiveProjection(ctx, sess.ID, msg.ID, "streaming body", calls))
	withCalls, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after calls", err)
	if len(withCalls[0].ToolCalls) != 1 || withCalls[0].ToolCalls[0].Name != "command" {
		t.Fatalf("tool_calls = %+v", withCalls[0].ToolCalls)
	}
	if withCalls[0].Seq != seq {
		t.Fatalf("seq after tool_calls = %d want %d", withCalls[0].Seq, seq)
	}

	testutil.FailErr(t, "PatchLiveProjection keep calls", st.PatchLiveProjection(ctx, sess.ID, msg.ID, "more prose", nil))
	kept, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after nil calls", err)
	if len(kept[0].ToolCalls) != 1 || kept[0].ToolCalls[0].Name != "command" {
		t.Fatalf("nil tool_calls persist wiped live calls: %+v", kept[0].ToolCalls)
	}

	for _, rows := range [][]api.Message{after, withCalls, kept} {
		got, original := rows[0], before[0]
		if got.ID != original.ID || got.Ord != original.Ord || got.Seq != original.Seq || !got.CreatedAt.Equal(original.CreatedAt) ||
			got.Role != original.Role || got.Visibility != original.Visibility || got.DraftStatus != original.DraftStatus {
			t.Fatalf("live projection changed durable metadata: got %+v, want %+v", got, original)
		}
	}

	var outboxRows int
	testutil.FailErr(t, "count outbox", database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM event_outbox`).Scan(&outboxRows))
	if outboxRows != 0 {
		t.Fatalf("outbox rows after PatchLiveProjection = %d want 0", outboxRows)
	}

	_, err = st.UpdateMessage(ctx, sess.ID, msg.ID, api.Message{
		ID:      msg.ID,
		Role:    api.MessageRoleAssistant,
		Content: "settled body",
	})
	testutil.FailErr(t, "UpdateMessage", err)
	waitOutboxEmpty(t, database)
	settled, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages settled", err)
	if settled[0].Seq <= seq {
		t.Fatalf("settle seq = %d want > %d", settled[0].Seq, seq)
	}
}

func TestMemoryPatchLiveProjectionDoesNotAdvanceSeq(t *testing.T) {
	st := NewMemory()
	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append", st.AppendMessages(ctx, sess.ID, api.Message{
		ID:          "live-1",
		Role:        api.MessageRoleAssistant,
		CreatedAt:   time.Now().UTC(),
		Visibility:  api.MessageVisibilityInternal,
		DraftStatus: api.DraftStatusLive,
	}))
	before, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages before", err)
	seq := before[0].Seq

	testutil.FailErr(t, "PatchLiveProjection", st.PatchLiveProjection(ctx, sess.ID, "live-1", "partial", nil))
	after, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after", err)
	if after[0].Content != "partial" {
		t.Fatalf("content = %q", after[0].Content)
	}
	if after[0].Seq != seq {
		t.Fatalf("seq = %d want %d", after[0].Seq, seq)
	}

	err = st.PatchLiveProjection(ctx, sess.ID, "missing", "x", nil)
	if err == nil {
		t.Fatal("PatchLiveProjection missing id: want error")
	}
}
