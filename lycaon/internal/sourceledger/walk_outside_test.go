package sourceledger

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func sessionWalkPaths(t *testing.T, store *Store, sessionID string, outside bool) map[string]Effect {
	t.Helper()
	walk, err := store.QueryWalk(t.Context(), "p1", Baseline{
		Kind: BaselineSession, SessionID: sessionID, WithOutsideChanges: outside,
	}, 50, 0, CommitLens{})
	testutil.FailErr(t, "query session walk", err)
	return effectsByPath(walk)
}

func TestSessionWalkWithOutsideChangesAdmitsUnownedDrift(t *testing.T) {
	store, ctx := openLedger(t)
	testdbseed.InsertSession(t, store.sqlDB, "s1", "p1")
	testdbseed.InsertSession(t, store.sqlDB, "s2", "p1")
	mustRecord(t, store, ctx, RecordInput{ProjectID: "p1", RootID: "r1", Path: "own.txt", SessionID: "s1", Turn: 1,
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent, After: []byte("a")})
	mustRecord(t, store, ctx, RecordInput{ProjectID: "p1", RootID: "r1", Path: "outside.txt",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal, After: []byte("b")})
	mustRecord(t, store, ctx, RecordInput{ProjectID: "p1", RootID: "r1", Path: "other-chat.txt", SessionID: "s2", Turn: 1,
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent, After: []byte("c")})
	mustRecord(t, store, ctx, RecordInput{ProjectID: "p1", RootID: "r1", Path: "worker.txt", BranchID: sourcebranch.ID("job-1"),
		JobID: "job-1", Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal, After: []byte("d")})

	own := sessionWalkPaths(t, store, "s1", false)
	if len(own) != 1 || own["own.txt"].ID == "" {
		t.Fatalf("plain session walk = %+v, want only the chat's own effect", own)
	}
	widened := sessionWalkPaths(t, store, "s1", true)
	if len(widened) != 2 || widened["own.txt"].ID == "" || widened["outside.txt"].ID == "" {
		t.Fatalf("widened session walk = %+v, want the chat's effect and the outside change", widened)
	}
}

// The span closes when the chat is archived; drift after that is not its story.
func TestSessionWalkWithOutsideChangesEndsAtArchive(t *testing.T) {
	store, ctx := openLedger(t)
	testdbseed.InsertSession(t, store.sqlDB, "s1", "p1")
	mustRecord(t, store, ctx, RecordInput{ProjectID: "p1", RootID: "r1", Path: "during.txt",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal, After: []byte("a")})
	_, err := store.sqlDB.ExecContext(ctx, `UPDATE sessions SET archived_at = ? WHERE id = 's1'`,
		db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "archive session", err)
	mustRecord(t, store, ctx, RecordInput{ProjectID: "p1", RootID: "r1", Path: "after.txt",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal,
		After: []byte("b"), TS: time.Now().UTC().Add(time.Minute)})

	widened := sessionWalkPaths(t, store, "s1", true)
	if len(widened) != 1 || widened["during.txt"].ID == "" {
		t.Fatalf("archived session walk = %+v, want only drift from before the archive", widened)
	}
}

// Drift from before the chat existed is not its story either.
func TestSessionWalkWithOutsideChangesStartsAtCreation(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{ProjectID: "p1", RootID: "r1", Path: "earlier.txt",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal,
		After: []byte("a"), TS: time.Now().UTC().Add(-time.Hour)})
	testdbseed.InsertSession(t, store.sqlDB, "s1", "p1")
	mustRecord(t, store, ctx, RecordInput{ProjectID: "p1", RootID: "r1", Path: "later.txt",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal,
		After: []byte("b"), TS: time.Now().UTC().Add(time.Minute)})

	widened := sessionWalkPaths(t, store, "s1", true)
	if len(widened) != 1 || widened["later.txt"].ID == "" {
		t.Fatalf("session walk = %+v, want only drift from after creation", widened)
	}
}
