package sourceledger

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDeletedPathDoesNotResurrectAnObservedReplacement(t *testing.T) {
	store, ctx := openLedger(t)
	// The deleted file sorts after the replacement by ID.
	const oldID = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	testutil.FailErr(t, "seed old identity", store.queries.InsertSourceFile(ctx, db.InsertSourceFileParams{
		ID: oldID, ProjectID: "p1", EntryKind: EntryKindFile, CreatedTs: "2026-09-12T10:00:00Z",
	}))
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "same.go", FileID: oldID,
		Op: api.SourceChangeOpDelete, Origin: api.SourceChangeOriginUser, Before: []byte("old\n"),
	})
	_, err := store.TrackFile(ctx, TrackInput{
		ProjectID: "p1", RootID: "r1", Path: "same.go", Content: []byte("replacement\n"),
	})
	testutil.FailErr(t, "observe replacement", err)
	if _, err := store.ResolveDeletedPath(ctx, "p1", sourcebranch.Trunk, "r1", "same.go"); !errors.Is(err, ErrHistoryNotFound) {
		t.Fatalf("old deletion survived a live observation: %v", err)
	}
}

func TestDeletedPathTracksLatestOccupantAndBranch(t *testing.T) {
	store, ctx := openLedger(t)
	record := func(branch sourcebranch.ID, op api.SourceChangeOp, before, after string) {
		t.Helper()
		mustRecord(t, store, ctx, RecordInput{ProjectID: "p1", BranchID: branch, RootID: "r1", Path: "same.go", Op: op, Origin: api.SourceChangeOriginUser, Before: []byte(before), After: []byte(after)})
	}
	record(sourcebranch.Trunk, api.SourceChangeOpCreate, "", "first\n")
	record(sourcebranch.Trunk, api.SourceChangeOpDelete, "first\n", "")
	first, err := store.ResolveDeletedPath(ctx, "p1", sourcebranch.Trunk, "r1", "same.go")
	testutil.FailErr(t, "resolve first deletion", err)
	content, err := store.DeletedPathContent(ctx, "p1", first)
	testutil.FailErr(t, "read first deletion", err)
	if content.Content != "first\n" || content.Availability != ContentAvailable {
		t.Fatalf("previous = %+v", content)
	}
	record(sourcebranch.Trunk, api.SourceChangeOpCreate, "", "replacement\n")
	if _, err := store.ResolveDeletedPath(ctx, "p1", sourcebranch.Trunk, "r1", "same.go"); !errors.Is(err, ErrHistoryNotFound) {
		t.Fatalf("live replacement = %v", err)
	}
	record(sourcebranch.Trunk, api.SourceChangeOpDelete, "replacement\n", "")
	second, err := store.ResolveDeletedPath(ctx, "p1", sourcebranch.Trunk, "r1", "same.go")
	testutil.FailErr(t, "resolve second deletion", err)
	if second.FileID == first.FileID {
		t.Fatal("recreated path reused deleted identity")
	}
	content, err = store.DeletedPathContent(ctx, "p1", second)
	testutil.FailErr(t, "read second deletion", err)
	if content.Content != "replacement\n" {
		t.Fatalf("replacement = %+v", content)
	}
	branch, err := sourcebranch.ForWorker("worker-1")
	testutil.FailErr(t, "worker branch", err)
	if _, err := store.ResolveDeletedPath(ctx, "p1", branch, "r1", "same.go"); !errors.Is(err, ErrHistoryNotFound) {
		t.Fatalf("worker inherited trunk deletion: %v", err)
	}
	record(branch, api.SourceChangeOpCreate, "", "worker\n")
	record(branch, api.SourceChangeOpDelete, "worker\n", "")
	worker, err := store.ResolveDeletedPath(ctx, "p1", branch, "r1", "same.go")
	testutil.FailErr(t, "resolve worker deletion", err)
	content, err = store.DeletedPathContent(ctx, "p1", worker)
	testutil.FailErr(t, "read worker deletion", err)
	if content.Content != "worker\n" {
		t.Fatalf("worker contents = %+v", content)
	}
	for _, query := range [][3]string{{"other", "r1", "same.go"}, {"p1", "other", "same.go"}, {"p1", "r1", "other.go"}} {
		if _, err := store.ResolveDeletedPath(ctx, query[0], sourcebranch.Trunk, query[1], query[2]); !errors.Is(err, ErrHistoryNotFound) {
			t.Fatalf("foreign query %v = %v", query, err)
		}
	}
	if _, err := store.DeletedPathContent(ctx, "other", first); !errors.Is(err, ErrHistoryNotFound) {
		t.Fatalf("foreign content = %v", err)
	}
}

func TestDeletedPathDoesNotResurrectAfterReplacementMoves(t *testing.T) {
	worker, err := sourcebranch.ForWorker("worker-1")
	testutil.FailErr(t, "worker branch", err)
	for name, branch := range map[string]sourcebranch.ID{"trunk": sourcebranch.Trunk, "worker": worker} {
		t.Run(name, func(t *testing.T) {
			store, ctx := openLedger(t)
			record := func(op api.SourceChangeOp, path, from, before, after string) {
				t.Helper()
				mustRecord(t, store, ctx, RecordInput{
					ProjectID: "p1", BranchID: branch, RootID: "r1", Path: path, FromPath: from,
					Op: op, Origin: api.SourceChangeOriginUser, Before: []byte(before), After: []byte(after),
				})
			}
			record(api.SourceChangeOpCreate, "same.go", "", "", "old\n")
			record(api.SourceChangeOpDelete, "same.go", "", "old\n", "")
			record(api.SourceChangeOpCreate, "same.go", "", "", "replacement\n")
			record(api.SourceChangeOpRename, "moved.go", "same.go", "replacement\n", "replacement\n")
			if _, err := store.ResolveDeletedPath(ctx, "p1", branch, "r1", "same.go"); !errors.Is(err, ErrHistoryNotFound) {
				t.Fatalf("moved replacement resurrected an old deletion: %v", err)
			}
			record(api.SourceChangeOpDelete, "moved.go", "", "replacement\n", "")
			if _, err := store.ResolveDeletedPath(ctx, "p1", branch, "r1", "same.go"); !errors.Is(err, ErrHistoryNotFound) {
				t.Fatalf("deletion elsewhere resurrected an old deletion: %v", err)
			}
		})
	}
}

func TestDeletedPathIgnoresUnsavedDocumentVersions(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "same.go", Op: api.SourceChangeOpDelete,
		Origin: api.SourceChangeOriginUser, Before: []byte("on disk\n"),
	})
	deleted, err := store.ResolveDeletedPath(ctx, "p1", sourcebranch.Trunk, "r1", "same.go")
	testutil.FailErr(t, "resolve deletion", err)
	prepared, err := store.PrepareHeldEdit(ctx, HeldEdit{
		ProjectID: "p1", FileID: deleted.FileID, RootID: "r1", Path: "same.go", Content: []byte("unsaved draft\n"),
	})
	testutil.FailErr(t, "prepare draft", err)
	defer prepared.Close()
	tx, err := store.sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin draft", err)
	defer tx.Rollback()
	_, err = prepared.CommitTx(ctx, tx)
	testutil.FailErr(t, "retain draft", err)
	testutil.FailErr(t, "commit draft", tx.Commit())
	resolved, err := store.ResolveDeletedPath(ctx, "p1", sourcebranch.Trunk, "r1", "same.go")
	testutil.FailErr(t, "resolve after draft", err)
	if resolved.VersionID != deleted.VersionID {
		t.Fatalf("unsaved draft replaced deletion: %+v", resolved)
	}
	content, err := store.DeletedPathContent(ctx, "p1", resolved)
	testutil.FailErr(t, "read retained contents", err)
	if content.Content != "on disk\n" {
		t.Fatalf("retained content = %q", content.Content)
	}
}
