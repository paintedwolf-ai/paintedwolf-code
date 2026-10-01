package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

// ordStoreFixture is one Store backend wired up with a fresh session, ready for
// the ord contract checks. The SQL and memory stores behave identically.
type ordStoreFixture struct {
	store     Store
	sessionID string
}

func newSQLOrdFixture(t *testing.T) ordStoreFixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "ord-parity.db")
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := store.NewSQL(sqlDB)
	sess, err := store.Create(context.Background(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	return ordStoreFixture{store: store, sessionID: sess.ID}
}

func newMemoryOrdFixture(t *testing.T) ordStoreFixture {
	t.Helper()
	store := store.NewMemory()
	sess, err := store.Create(context.Background(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	return ordStoreFixture{store: store, sessionID: sess.ID}
}

// AppendMessages mints a strictly increasing per-session creation ordinal in both
// stores; ord is the integer Den sorts by.
func TestStoreOrdMonotonicAppend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture func(t *testing.T) ordStoreFixture
	}{
		{"sql", newSQLOrdFixture},
		{"memory", newMemoryOrdFixture},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fixture(t)
			ctx := context.Background()
			batch := []api.Message{
				{Role: api.MessageRoleUser, Content: "first", CreatedAt: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)},
				{Role: api.MessageRoleAssistant, Content: "second", CreatedAt: time.Date(2026, 7, 8, 12, 0, 1, 0, time.UTC)},
				{Role: api.MessageRoleTool, Content: "third", CreatedAt: time.Date(2026, 7, 8, 12, 0, 2, 0, time.UTC)},
			}
			testutil.FailErr(t, "append batch", f.store.AppendMessages(ctx, f.sessionID, batch...))

			got, err := f.store.GetMessages(ctx, f.sessionID)
			testutil.FailErr(t, "get messages", err)
			if len(got) != len(batch) {
				t.Fatalf("got %d messages, want %d", len(got), len(batch))
			}
			for i, m := range got {
				if m.Ord != int64(i+1) {
					t.Fatalf("message %d ord = %d, want %d (strictly increasing from 1)", i, m.Ord, i+1)
				}
			}
			// A second append continues the ordinal; it does not reset.
			testutil.FailErr(t, "append second batch", f.store.AppendMessages(ctx, f.sessionID, api.Message{
				Role: api.MessageRoleUser, Content: "fourth", CreatedAt: time.Now().UTC(),
			}))
			got2, err := f.store.GetMessages(ctx, f.sessionID)
			testutil.FailErr(t, "get messages after second append", err)
			if got2[len(got2)-1].Ord != int64(len(batch)+1) {
				t.Fatalf("fourth message ord = %d, want %d (ordinal continues across appends)", got2[len(got2)-1].Ord, len(batch)+1)
			}
		})
	}
}

// Patches preserve creation time and ordinal while advancing the mutation sequence.
func TestStoreOrdAndTSSurvivePatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture func(t *testing.T) ordStoreFixture
	}{
		{"sql", newSQLOrdFixture},
		{"memory", newMemoryOrdFixture},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fixture(t)
			ctx := context.Background()
			created := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
			testutil.FailErr(t, "append", f.store.AppendMessages(ctx, f.sessionID, api.Message{
				ID:        "draft-1",
				Role:      api.MessageRoleAssistant,
				Content:   "streaming...",
				CreatedAt: created,
			}))

			before, err := f.store.GetMessages(ctx, f.sessionID)
			testutil.FailErr(t, "get messages before patch", err)
			if len(before) != 1 {
				t.Fatalf("got %d messages, want 1", len(before))
			}
			wantOrd := before[0].Ord
			wantTS := before[0].CreatedAt
			wantSeq := before[0].Seq
			if wantOrd == 0 {
				t.Fatalf("ord not minted at append: %d", wantOrd)
			}

			// A patch with a zero ts and a mutated body: the caller does not know
			// the creation time and cannot overwrite it.
			_, err = f.store.UpdateMessage(ctx, f.sessionID, "draft-1", api.Message{
				ID:        "draft-1",
				Role:      api.MessageRoleAssistant,
				Content:   "committed body",
				CreatedAt: time.Time{},
			})
			testutil.FailErr(t, "patch", err)

			after, err := f.store.GetMessages(ctx, f.sessionID)
			testutil.FailErr(t, "get messages after patch", err)
			if len(after) != 1 {
				t.Fatalf("got %d messages after patch, want 1", len(after))
			}
			if after[0].Ord != wantOrd {
				t.Fatalf("ord changed across patch: was %d, now %d — ord is immutable", wantOrd, after[0].Ord)
			}
			if !after[0].CreatedAt.Equal(wantTS) {
				t.Fatalf("ts changed across patch: was %v, now %v — ts is the creation timestamp, immutable on patch", wantTS, after[0].CreatedAt)
			}
			if after[0].Seq <= wantSeq {
				t.Fatalf("seq did not advance across patch: was %d, now %d — seq is the mutation clock and must advance", wantSeq, after[0].Seq)
			}
			if after[0].Content != "committed body" {
				t.Fatalf("patched content = %q, want %q", after[0].Content, "committed body")
			}
		})
	}
}
