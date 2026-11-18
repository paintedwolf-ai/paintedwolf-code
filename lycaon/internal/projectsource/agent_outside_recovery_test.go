package projectsource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOutsideEffectAndReferenceRecoverTogether(t *testing.T) {
	ctx := t.Context()
	service, p, _, _ := sourceMutationFixture(t)
	database := service.Journal.db
	testdbseed.InsertSession(t, database, "chat", p.ID)
	outsideID, err := db.New(database).GetProjectOutsideRootID(ctx, p.ID)
	testutil.FailErr(t, "read outside namespace", err)
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	before, after := []byte("before"), []byte("after")
	testutil.FailErr(t, "seed external file", os.WriteFile(path, before, 0o600))
	record := sourceledger.RecordInput{
		ProjectID: p.ID, SessionID: "chat", ToolCallID: "outside-call", Turn: 1,
		RecordLocation: sourceledger.RecordLocation{RootID: outsideID, Path: "outside-fixture/note.txt"},
		Op:             api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		Before: before, After: after, BeforeSHA256: textfile.SHA256(before), AfterSHA256: textfile.SHA256(after),
	}
	effect := sourceeffect.Plan{
		Record: record, Target: fseffect.Location{Root: root, Rel: "note.txt"},
		Change: sourcefeed.Change{ProjectID: p.ID, WorkspaceID: p.WorkspaceID(),
			WorkspaceKind: api.SourceWorkspaceKindProject, RootID: record.RootID, Path: record.Path,
			Op: record.Op, Origin: record.Origin, AbsPath: path},
	}
	pending, err := service.PrepareEffect(ctx, effect)
	testutil.FailErr(t, "prepare outside write", err)
	_, err = database.ExecContext(ctx, `CREATE TRIGGER reject_chat_reference BEFORE INSERT ON source_chat_files
		BEGIN SELECT RAISE(ABORT, 'chat reference unavailable'); END`)
	testutil.FailErr(t, "inject reference failure", err)
	testutil.FailErr(t, "publish outside bytes", os.WriteFile(path, after, 0o600))
	err = pending.Finish(ctx, nil)
	var applied *sourceeffect.AppliedError
	if !errors.As(err, &applied) || applied.OperationID != pending.ID() {
		t.Fatalf("published write reported as %v", err)
	}
	checkRows := func(want int) {
		t.Helper()
		var ops, refs, effects int
		err := database.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM source_operations WHERE operation_key = ?),
			(SELECT count(*) FROM source_chat_files WHERE session_id = 'chat' AND changed = 1),
			(SELECT count(*) FROM source_effects WHERE root_id = ?)`, pending.ID(), outsideID).Scan(&ops, &refs, &effects)
		testutil.FailErr(t, "count operation, reference and effect", err)
		if ops != want || refs != want || effects != want {
			t.Fatalf("operation, reference and effect counts = %d, %d, %d; want %d each", ops, refs, effects, want)
		}
	}
	checkRows(0)

	_, err = database.ExecContext(ctx, `DROP TRIGGER reject_chat_reference`)
	testutil.FailErr(t, "restore reference publication", err)
	testutil.FailErr(t, "make a later external edit", os.WriteFile(path, []byte("later"), 0o600))
	store := service.settlement.recorder.(*sourceledger.Store)
	restarted := NewSourceMutationService(database, store)
	for range 2 {
		testutil.FailErr(t, "recover outside write", restarted.Recover(ctx))
		checkRows(1)
	}
	effect.Record.OperationID = pending.ID()
	if _, err := restarted.PrepareEffect(ctx, effect); !errors.Is(err, ErrSourceMutationConflict) {
		t.Fatalf("readmitted published operation: %v", err)
	}
	var effectID string
	testutil.FailErr(t, "find recovered effect", database.QueryRowContext(ctx,
		`SELECT id FROM source_effects WHERE root_id = ?`, outsideID).Scan(&effectID))
	comparison, err := store.Comparisons.CompareEffect(ctx, p.ID, effectID)
	testutil.FailErr(t, "compare recovered effect", err)
	if comparison.Before.Content != "before" || comparison.After.Content != "after" {
		t.Fatalf("recovered comparison = %+v", comparison)
	}
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read file after recovery", err)
	if string(body) != "later" {
		t.Fatalf("recovery overwrote a later edit: %q", body)
	}
}
