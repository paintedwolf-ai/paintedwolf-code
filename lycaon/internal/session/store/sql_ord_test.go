package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestHydrationReturnsOrdOrderUnderOutOfOrderTS(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "ord-hydration.db")
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	msgs := []api.Message{
		{ID: "m-1", Role: api.MessageRoleUser, Content: "first"},
		{ID: "m-2", Role: api.MessageRoleAssistant, Content: "second"},
		{ID: "m-3", Role: api.MessageRoleTool, Content: "third"},
	}
	testutil.FailErr(t, "append", store.AppendMessages(ctx, sess.ID, msgs...))

	// Physical rows and timestamps run opposite to creation order.
	scramble := []struct {
		id string
		ts string
	}{
		{"m-1", "2026-07-08T12:00:03Z"},
		{"m-2", "2026-07-08T12:00:02Z"},
		{"m-3", "2026-07-08T12:00:00Z"},
	}
	for i, r := range scramble {
		if _, err := sqlDB.ExecContext(ctx, `UPDATE messages SET ts = ?, rowid = ? WHERE session_id = ? AND id = ?`, r.ts, 100-i, sess.ID, r.id); err != nil {
			testutil.FailErr(t, "scramble timestamp and physical order for "+r.id, err)
		}
	}

	got, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3", len(got))
	}
	wantIDs := []string{"m-1", "m-2", "m-3"}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Fatalf("hydration order[%d] = %q (ord=%d, ts=%v), want %q — rows must come back in ord order, not ts order",
				i, got[i].ID, got[i].Ord, got[i].CreatedAt, want)
		}
		if got[i].Ord != int64(i+1) {
			t.Fatalf("message %d ord = %d, want %d", i, got[i].Ord, i+1)
		}
	}
	before, after := int64(3), int64(1)
	for _, tc := range []struct {
		name  string
		query api.TranscriptPageQuery
		ids   []string
	}{
		{"tail", api.TranscriptPageQuery{Limit: 2}, []string{"m-2", "m-3"}},
		{"before", api.TranscriptPageQuery{Limit: 2, Before: &before}, []string{"m-1", "m-2"}},
		{"after", api.TranscriptPageQuery{Limit: 2, After: &after}, []string{"m-2", "m-3"}},
	} {
		page, err := store.GetTranscriptPage(ctx, sess.ID, tc.query)
		testutil.FailErr(t, tc.name+" page", err)
		if len(page.Messages) != len(tc.ids) {
			t.Fatalf("%s page has %d messages, want %d", tc.name, len(page.Messages), len(tc.ids))
		}
		for i, id := range tc.ids {
			if page.Messages[i].ID != id {
				t.Fatalf("%s page message %d = %s, want %s", tc.name, i, page.Messages[i].ID, id)
			}
		}
	}
}

func TestUpdateMessagePreservesCreationMetadata(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "patch-metadata.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	st := NewSQL(sqlDB)
	ctx := t.Context()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	testutil.FailErr(t, "append messages", st.AppendMessages(ctx, sess.ID,
		api.Message{ID: "first", Role: api.MessageRoleAssistant, Content: "original", CreatedAt: created},
		api.Message{ID: "second", Role: api.MessageRoleUser, Content: "neighbor", CreatedAt: created}))
	before, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read initial messages", err)
	if len(before) != 2 {
		t.Fatalf("initial transcript has %d messages, want 2", len(before))
	}
	for _, stamp := range []time.Time{{}, created.Add(-time.Hour)} {
		patch := before[0]
		patch.Ord, patch.Seq, patch.CreatedAt = 999, 999, stamp
		patch.Content, patch.DraftStatus = "revised", api.DraftStatusRejected
		updated, err := st.UpdateMessage(ctx, sess.ID, patch.ID, patch)
		testutil.FailErr(t, "patch message", err)
		rows, err := st.GetMessages(ctx, sess.ID)
		testutil.FailErr(t, "read patched messages", err)
		if len(rows) != 2 || rows[0].ID != "first" || rows[1].ID != "second" {
			t.Fatalf("patch changed transcript membership or order: %+v", rows)
		}
		for _, got := range []api.Message{updated, rows[0]} {
			if got.Ord != before[0].Ord || !got.CreatedAt.Equal(created) || got.Seq <= before[0].Seq || got.Seq == 999 ||
				got.Content != "revised" || got.DraftStatus != api.DraftStatusRejected {
				t.Fatalf("patch metadata = %+v, before = %+v", got, before[0])
			}
		}
		if rows[1].Seq != before[1].Seq || rows[1].Content != before[1].Content {
			t.Fatalf("patch changed neighboring message: %+v", rows[1])
		}
		before = rows
	}
}

func TestAppendMessagesStampsStrictlyIncreasingOrdSQLOnly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ord-monotonic.db")
	sqlDB := testdbfixture.OpenPath(t, dbPath)
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	for i := 0; i < 4; i++ {
		testutil.FailErr(t, "append", store.AppendMessages(ctx, sess.ID, api.Message{
			Role: api.MessageRoleUser, Content: "m", CreatedAt: time.Now().UTC(),
		}))
	}

	// Read physical insertion order to inspect stored ordinals.
	rows, err := sqlDB.QueryContext(ctx, `SELECT ord FROM messages WHERE session_id = ? ORDER BY rowid`, sess.ID)
	testutil.FailErr(t, "query ord column", err)
	defer rows.Close()
	var prev, count int
	for rows.Next() {
		var ord int64
		testutil.FailErr(t, "scan ord", rows.Scan(&ord))
		count++
		if ord <= int64(prev) {
			t.Fatalf("ord not strictly increasing: prev=%d, ord=%d", prev, ord)
		}
		prev = int(ord)
	}
	testutil.FailErr(t, "rows.Err", rows.Err())
	if count != 4 {
		t.Fatalf("scanned %d ord values, want 4", count)
	}

	testutil.FailErr(t, "close for reopen", sqlDB.Close())
	sqlDB2 := testdbfixture.OpenPath(t, dbPath)
	var maxOrd int64
	testutil.FailErr(t, "query max ord after reopen",
		sqlDB2.QueryRowContext(ctx, `SELECT MAX(ord) FROM messages WHERE session_id = ?`, sess.ID).Scan(&maxOrd))
	if maxOrd != 4 {
		t.Fatalf("max ord after reopen = %d, want 4 (ord persists as a column)", maxOrd)
	}
}

func TestAppendMessagesOrdContiguous(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "ord-contiguous.db")
	sqlDB := testdbfixture.OpenPath(t, dbPath)
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := NewSQL(sqlDB)
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	for i := 0; i < 6; i++ {
		if err := store.AppendMessages(ctx, sess.ID, api.Message{
			ID:      fmt.Sprintf("m-%d", i+1),
			Role:    api.MessageRoleUser,
			Content: fmt.Sprintf("msg %d", i+1),
		}); err != nil {
			testutil.FailErr(t, "append message", err)
		}
	}
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) != 6 {
		t.Fatalf("message count = %d want 6", len(msgs))
	}
	for i, msg := range msgs {
		want := int64(i + 1)
		if msg.Ord != want {
			t.Fatalf("message[%d] ord = %d want %d — ords must be contiguous", i, msg.Ord, want)
		}
	}
}
