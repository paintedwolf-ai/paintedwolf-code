package store_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTranscriptPageTailAndCursors(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{ProjectID: "p1", Posture: api.SessionPostureBuild}, "p1")
	testutil.FailErr(t, "create session", err)

	msgs := make([]api.Message, 0, 12)
	for i := 0; i < 12; i++ {
		msgs = append(msgs, api.Message{Role: api.MessageRoleUser, Content: "m"})
	}
	testutil.FailErr(t, "append", mem.AppendMessages(ctx, sess.ID, msgs...))

	tail, err := mem.GetTranscriptPage(ctx, sess.ID, api.TranscriptPageQuery{Limit: 5})
	testutil.FailErr(t, "tail page", err)
	if len(tail.Messages) != 5 {
		t.Fatalf("tail len = %d, want 5", len(tail.Messages))
	}
	if tail.BeforeCursor == "" || tail.AfterCursor != "" {
		t.Fatalf("tail cursors: before=%v after=%v", tail.BeforeCursor, tail.AfterCursor)
	}
	if tail.Messages[0].Ord != 8 {
		t.Fatalf("oldest_ord = %d, want 8", tail.Messages[0].Ord)
	}
	if tail.Messages[len(tail.Messages)-1].Ord != 12 {
		t.Fatalf("newest_ord = %d, want 12", tail.Messages[len(tail.Messages)-1].Ord)
	}
	for i := 1; i < len(tail.Messages); i++ {
		if tail.Messages[i].Ord <= tail.Messages[i-1].Ord {
			t.Fatalf("tail not ascending at %d", i)
		}
	}

	before := tail.Messages[0].Ord
	older, err := mem.GetTranscriptPage(ctx, sess.ID, api.TranscriptPageQuery{Limit: 5, Before: &before})
	testutil.FailErr(t, "before page", err)
	if len(older.Messages) != 5 {
		t.Fatalf("before len = %d, want 5", len(older.Messages))
	}
	if older.BeforeCursor == "" || older.AfterCursor == "" {
		t.Fatalf("before mid-window cursors: before=%v after=%v", older.BeforeCursor, older.AfterCursor)
	}
	if older.Messages[len(older.Messages)-1].Ord >= before {
		t.Fatalf("before page newest=%d must be < %d", older.Messages[len(older.Messages)-1].Ord, before)
	}

	after := older.Messages[len(older.Messages)-1].Ord
	newer, err := mem.GetTranscriptPage(ctx, sess.ID, api.TranscriptPageQuery{Limit: 5, After: &after})
	testutil.FailErr(t, "after page", err)
	if len(newer.Messages) != 5 {
		t.Fatalf("after len = %d, want 5", len(newer.Messages))
	}
	if newer.Messages[0].Ord <= after {
		t.Fatalf("after page oldest=%d must be > %d", newer.Messages[0].Ord, after)
	}
}

func TestTranscriptPageDefaultLimitIsBundledConst(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{ProjectID: "p1", Posture: api.SessionPostureBuild}, "p1")
	testutil.FailErr(t, "create session", err)

	n := api.DefaultTranscriptPageLimit + 25
	batch := make([]api.Message, 0, n)
	for i := 0; i < n; i++ {
		batch = append(batch, api.Message{Role: api.MessageRoleUser, Content: "m"})
	}
	testutil.FailErr(t, "append", mem.AppendMessages(ctx, sess.ID, batch...))

	page, err := mem.GetTranscriptPage(ctx, sess.ID, api.TranscriptPageQuery{})
	testutil.FailErr(t, "default page", err)
	if len(page.Messages) != api.DefaultTranscriptPageLimit {
		t.Fatalf("default limit len = %d, want %d", len(page.Messages), api.DefaultTranscriptPageLimit)
	}
	if page.BeforeCursor == "" {
		t.Fatal("expected before_cursor on truncated tail")
	}
}
