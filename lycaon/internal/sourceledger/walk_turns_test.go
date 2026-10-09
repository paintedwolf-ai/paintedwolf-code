package sourceledger

import (
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func insertWalkMessage(t *testing.T, store *Store, id, session, kind, visibility, content string, ord int64) {
	t.Helper()
	testdbseed.InsertSessionEntry(t, store.sqlDB, "entry-"+id, session, "utterance", id, ord)
	_, err := store.sqlDB.ExecContext(t.Context(), `
		INSERT INTO messages (id, entry_id, session_id, role, content, origin, authority,
		 trust_tier, kind, visibility, ord, ts)
		VALUES (?, ?, ?, 'user', ?, 'user', 'user', 'trusted', ?, ?, ?, ?)
	`, id, "entry-"+id, session, content, kind, visibility, ord, db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "insert walk message", err)
}

func TestWalkChaptersUseCompleteTranscriptOrdinals(t *testing.T) {
	store, ctx := openLedger(t)
	testdbseed.InsertSession(t, store.sqlDB, "s1", "p1")
	insertWalkMessage(t, store, "first", "s1", "", "transcript", "First request", 1)
	insertWalkMessage(t, store, "host", "s1", "", "internal", "Host control", 2)
	insertWalkMessage(t, store, "continuation", "s1", "user_continuation", "transcript", "More detail", 3)
	insertWalkMessage(t, store, "workflow", "s1", "workflow_boundary", "transcript", "Workflow", 4)
	insertWalkMessage(t, store, "second", "s1", "", "transcript", "Fix\n the editor", 5)
	for i := 1; i <= 2; i++ {
		mustRecord(t, store, ctx, RecordInput{
			ProjectID: "p1", RootID: "r1", Path: fmt.Sprintf("file-%d", i),
			SessionID: "s1", Turn: i, OperationID: fmt.Sprintf("op-%d", i),
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("hello"),
		})
	}
	page, err := store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "s1"}, 1, 0, CommitLens{})
	testutil.FailErr(t, "load newest walk page", err)
	if len(page.Turns) != 1 || page.Turns[0].Turn != 2 || page.Turns[0].MessageID != "second" || page.Turns[0].Prompt != "Fix the editor" {
		t.Fatalf("newest page turns = %+v", page.Turns)
	}
	page, err = store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "s1"}, 1, page.NextBeforeOrdinal, CommitLens{})
	testutil.FailErr(t, "load older walk page", err)
	if len(page.Turns) != 1 || page.Turns[0].Turn != 1 || page.Turns[0].MessageID != "first" {
		t.Fatalf("older page turns = %+v", page.Turns)
	}
}

func TestWalkChapterMetadataCannotCrossProjectBoundary(t *testing.T) {
	store, ctx := openLedger(t)
	testdbseed.InsertSession(t, store.sqlDB, "other", "p2")
	insertWalkMessage(t, store, "private-message", "other", "", "transcript", "Other project request", 1)
	turns, err := store.Walk.walkTurns(ctx, "p1", []Effect{{SessionID: "other", Turn: 1}}, nil)
	testutil.FailErr(t, "load walk turn metadata", err)
	if len(turns) != 0 {
		t.Fatalf("cross-project metadata = %+v", turns)
	}
}
