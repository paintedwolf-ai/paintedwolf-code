package sourceledger

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Root movement preserves file identity.
func TestFileHistorySurvivesTheRootFolderMoving(t *testing.T) {
	store, ctx := openLedger(t)

	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "notes/hello.txt"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		After: []byte("first\n")})
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "notes/hello.txt"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser,
		Before: []byte("first\n"), After: []byte("second\n")})
	fileID, _ := mustResolve(t, store, ctx, "notes/hello.txt")
	before, err := store.History.QueryFileVersions(ctx, "p1", fileID, 50, 0)
	testutil.FailErr(t, "list versions before the move", err)
	if len(before.Versions) < 2 {
		t.Fatalf("expected the file's states before the move, got %d", len(before.Versions))
	}

	_, err = store.sqlDB.ExecContext(ctx,
		`UPDATE project_roots SET path = ?, kind = 'attached' WHERE id = 'r1'`,
		t.TempDir())
	testutil.FailErr(t, "move the root's folder", err)

	afterID, _ := mustResolve(t, store, ctx, "notes/hello.txt")
	if afterID != fileID {
		t.Fatalf("the file forked its identity across the move: %s then %s", fileID, afterID)
	}
	after, err := store.History.QueryFileVersions(ctx, "p1", afterID, 50, 0)
	testutil.FailErr(t, "list versions after the move", err)
	if len(after.Versions) != len(before.Versions) {
		t.Fatalf("versions after the move = %d, want the %d recorded before it",
			len(after.Versions), len(before.Versions))
	}
}

// Unrelated roots do not affect existing file identity.
func TestFileHistorySurvivesAnUnrelatedRootAttaching(t *testing.T) {
	store, ctx := openLedger(t)

	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "a.txt"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		After: []byte("v1\n")})
	fileID, _ := mustResolve(t, store, ctx, "a.txt")

	_, err := store.sqlDB.ExecContext(ctx, `
		INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at, kind)
		VALUES ('r2', 'p1', ?, 'secondary', 0, ?, 'attached')
	`, t.TempDir(), db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "attach a second root", err)

	afterAttach, _ := mustResolve(t, store, ctx, "a.txt")
	if afterAttach != fileID {
		t.Fatalf("attaching a second root forked the first root's file: %s then %s", fileID, afterAttach)
	}

	_, err = store.sqlDB.ExecContext(ctx, `DELETE FROM project_roots WHERE id = 'r2'`)
	testutil.FailErr(t, "detach the second root", err)

	afterDetach, _ := mustResolve(t, store, ctx, "a.txt")
	if afterDetach != fileID {
		t.Fatalf("detaching forked the file: %s then %s", fileID, afterDetach)
	}
	versions, err := store.History.QueryFileVersions(ctx, "p1", fileID, 50, 0)
	testutil.FailErr(t, "list versions", err)
	if len(versions.Versions) == 0 {
		t.Fatal("the file reports no retained states after the root set changed")
	}
}

// Worker branches extend trunk file identity without moving its head.
func TestWorkerBranchExtendsTrunkFileIdentity(t *testing.T) {
	store, ctx := openLedger(t)

	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "shared.txt"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		After: []byte("trunk\n")})
	trunkFileID, trunkVersionID := mustResolve(t, store, ctx, "shared.txt")

	worker, err := sourcebranch.ForWorker("job-1")
	testutil.FailErr(t, "resolve the worker branch", err)
	testutil.FailErr(t, "record the worker write", store.Record(ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "shared.txt"},
		ProjectID:      "p1", BranchID: worker,
		JobID: "job-1", Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		Before: []byte("trunk\n"), After: []byte("overlay\n")}))

	workerHead, err := store.History.ResolveHead(ctx, "p1", worker, "r1", "shared.txt")
	testutil.FailErr(t, "resolve the worker head", err)
	if workerHead.FileID != trunkFileID {
		t.Fatalf("worker branch started a second file: %s want %s", workerHead.FileID, trunkFileID)
	}
	if workerHead.VersionID == trunkVersionID {
		t.Fatal("worker branch did not advance its own head")
	}

	trunkHead, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "r1", "shared.txt")
	testutil.FailErr(t, "resolve the trunk head", err)
	if trunkHead.VersionID != trunkVersionID {
		t.Fatalf("the worker's write moved the trunk head to %s", trunkHead.VersionID)
	}
}

// Pins retain their boundary without copying per-file state.
func TestPinRetainsTrunkComparisonBoundary(t *testing.T) {
	store, ctx := openLedger(t)

	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "shared.txt"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		After: []byte("trunk\n")})
	worker, err := sourcebranch.ForWorker("job-1")
	testutil.FailErr(t, "resolve the worker branch", err)
	testutil.FailErr(t, "record the worker write", store.Record(ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "shared.txt"},
		ProjectID:      "p1", BranchID: worker,
		JobID: "job-1", Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		Before: []byte("trunk\n"), After: []byte("overlay\n")}))

	pin, err := store.Checkpoints.CreatePin(ctx, "p1", "boundary")
	testutil.FailErr(t, "create the pin", err)

	fileID, _ := mustResolve(t, store, ctx, "shared.txt")
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "shared.txt"},
		ProjectID:      "p1", Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser,
		Before: []byte("trunk\n"), After: []byte("later\n"),
	})
	comparison, err := store.Comparisons.CompareScope(ctx, "p1", sourcebranch.Trunk, Baseline{Kind: BaselinePin, PinID: pin.ID}, fileID, ScopeComparisonOptions{})
	testutil.FailErr(t, "compare retained pin", err)
	if !comparison.InRange || comparison.Before.Content != "trunk\n" || comparison.After.Content != "later\n" {
		t.Fatalf("pin lost its trunk boundary: %+v", comparison)
	}
}
