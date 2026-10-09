package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func openLedger(t *testing.T) (*Store, context.Context) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, "p1")
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at, kind)
		VALUES ('r1', 'p1', '/tmp/source-ledger-test', 'source-ledger-test', 1, ?, 'attached')
	`, db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "insert root", err)
	return New(sqlDB, t.TempDir()), context.Background()
}

func TestNamedPinsRemainUntilExplicitDeletion(t *testing.T) {
	store, ctx := openLedger(t)
	for range 64 {
		_, err := store.Checkpoints.CreatePin(ctx, "p1", "saved boundary")
		testutil.FailErr(t, "create pin", err)
	}
	var pins []Pin
	query := PinPageQuery{Limit: 17}
	for {
		page, err := store.Checkpoints.ListPinsPage(ctx, "p1", query)
		testutil.FailErr(t, "list pins", err)
		pins = append(pins, page.Pins...)
		if page.NextID == "" {
			break
		}
		query.BeforeCreatedTS, query.BeforeID = page.NextCreatedTS, page.NextID
	}
	if len(pins) != 64 {
		t.Fatalf("pins = %d want 64", len(pins))
	}
}

func mustRecord(t *testing.T, store *Store, ctx context.Context, input RecordInput) {
	t.Helper()
	testutil.FailErr(t, "record source effect", store.Record(ctx, input))
}

func mustResolve(t *testing.T, store *Store, ctx context.Context, path string) (string, string) {
	t.Helper()
	fileID, versionID, err := store.History.ResolveFile(ctx, "p1", sourcebranch.Trunk, "r1", path)
	testutil.FailErr(t, "resolve file", err)
	return fileID, versionID
}

func latestEffectID(t *testing.T, store *Store, ctx context.Context) string {
	t.Helper()
	rows, err := store.queries.ListSourceEffectsForProject(ctx, db.ListSourceEffectsForProjectParams{
		ProjectID: "p1", PageLimit: 1,
	})
	testutil.FailErr(t, "list effects", err)
	if len(rows) != 1 {
		t.Fatal("missing source effect")
	}
	return rows[0].EffectID
}

func TestMovePreservesFileIdentityAndExactComparison(t *testing.T) {
	store, ctx := openLedger(t)
	content := []byte("the door\n")
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "story.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "create-story", After: content,
	})
	fileID, beforeVersionID := mustResolve(t, store, ctx, "story.txt")
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "test/story.txt", FromPath: "story.txt",
		Op: api.SourceChangeOpRename, Origin: api.SourceChangeOriginUser,
		OperationID: "move-story",
	})
	movedFileID, afterVersionID := mustResolve(t, store, ctx, "test/story.txt")
	if movedFileID != fileID || afterVersionID == beforeVersionID {
		t.Fatalf("move identity/version = %s/%s, want %s/new", movedFileID, afterVersionID, fileID)
	}
	comparison, err := store.Comparisons.CompareEffect(ctx, "p1", latestEffectID(t, store, ctx))
	testutil.FailErr(t, "compare move", err)
	if !comparison.LocationChanged || comparison.Before.Path != "story.txt" ||
		comparison.After.Path != "test/story.txt" ||
		comparison.Before.Content != string(content) || comparison.After.Content != string(content) {
		t.Fatalf("move comparison = %+v", comparison)
	}
	if comparison.Before.Availability != ContentAvailable || comparison.After.Availability != ContentAvailable {
		t.Fatalf("move content availability = %s/%s", comparison.Before.Availability, comparison.After.Availability)
	}
}

func TestDirectoryMoveAdvancesOnlyTrackedDescendants(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "src", EntryKind: EntryKindDirectory,
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "create-directory",
	})
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "src/main.go",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "create-child", After: []byte("package main\n"),
	})
	childFileID, childVersionID := mustResolve(t, store, ctx, "src/main.go")

	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "app", FromPath: "src",
		EntryKind: EntryKindDirectory, Op: api.SourceChangeOpRename,
		Origin: api.SourceChangeOriginUser, OperationID: "move-directory",
	})

	movedFileID, movedVersionID := mustResolve(t, store, ctx, "app/main.go")
	if movedFileID != childFileID || movedVersionID == childVersionID {
		t.Fatalf("descendant identity/version = %s/%s, want %s/new", movedFileID, movedVersionID, childFileID)
	}
	if _, _, err := store.History.ResolveFile(ctx, "p1", sourcebranch.Trunk, "r1", "src/main.go"); !errors.Is(err, ErrHistoryNotFound) {
		t.Fatalf("old descendant path error = %v", err)
	}
	var files int
	testutil.FailErr(t, "count sparse files", store.sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM source_files`).Scan(&files))
	if files != 2 {
		t.Fatalf("directory move materialized untracked descendants: files = %d", files)
	}
}

func TestDeleteAndRecreatePathMintsNewFileIdentity(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "note.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "create-old", After: []byte("old\n"),
	})
	oldFileID, _ := mustResolve(t, store, ctx, "note.txt")
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "note.txt", FileID: oldFileID,
		Op: api.SourceChangeOpDelete, Origin: api.SourceChangeOriginUser,
		OperationID: "delete-old", Before: []byte("old\n"),
	})
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "note.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "create-new", After: []byte("new\n"),
	})
	newFileID, _ := mustResolve(t, store, ctx, "note.txt")
	if newFileID == oldFileID {
		t.Fatal("delete and recreate reused the deleted file identity")
	}
	oldComparison, err := store.Comparisons.CompareScope(ctx, "p1", sourcebranch.Trunk, Baseline{}, oldFileID, ScopeComparisonOptions{})
	testutil.FailErr(t, "compare deleted identity", err)
	if !oldComparison.InRange || oldComparison.After.Availability != ContentAbsent {
		t.Fatalf("deleted identity comparison = %+v", oldComparison)
	}
}

func TestTrackingAnOpenFileCreatesBaselineWithoutFakeWalkEffect(t *testing.T) {
	store, ctx := openLedger(t)
	tracked, err := store.TrackFile(ctx, TrackInput{
		ProjectID: "p1", RootID: "r1", Path: "opened.txt", Content: []byte("first\n"),
	})
	testutil.FailErr(t, "track opened file", err)
	var effects int
	testutil.FailErr(t, "count baseline effects", store.sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM source_effects`).Scan(&effects))
	if effects != 0 {
		t.Fatalf("opening a file invented %d Walk effects", effects)
	}

	updated, err := store.TrackFile(ctx, TrackInput{
		ProjectID: "p1", RootID: "r1", Path: "opened.txt", Content: []byte("second\n"),
	})
	testutil.FailErr(t, "observe changed open file", err)
	if updated.FileID != tracked.FileID || updated.VersionID == tracked.VersionID {
		t.Fatalf("tracked identity/version = %+v, want file %s and a new version", updated, tracked.FileID)
	}
	testutil.FailErr(t, "count observed effects", store.sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM source_effects`).Scan(&effects))
	if effects != 1 {
		t.Fatalf("changed observation effects = %d", effects)
	}
}

func TestWalkEffectOwnsBothWriteEndpoints(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "main.go",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		OperationID: "write-main", Before: []byte("package old\n"), After: []byte("package main\n"),
	})
	comparison, err := store.Comparisons.CompareEffect(ctx, "p1", latestEffectID(t, store, ctx))
	testutil.FailErr(t, "compare write", err)
	if comparison.Before.Content != "package old\n" || comparison.After.Content != "package main\n" {
		t.Fatalf("comparison = %+v", comparison)
	}
	if comparison.Before.VersionID == "" || comparison.After.VersionID == "" {
		t.Fatalf("effect endpoints are not explicit: %+v", comparison)
	}
}

func TestVersionHistoryContainsWorkerBranchWithoutAnotherTab(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "main.go",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "create-main", After: []byte("primary\n"),
	})
	fileID, primaryVersionID := mustResolve(t, store, ctx, "main.go")
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", BranchID: "ws_worker_1",
		RootID: "r1", Path: "main.go", FileID: fileID,
		DerivedFromVersionID: primaryVersionID,
		Op:                   api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		JobID: "job-1", OperationID: "worker-write",
		Before: []byte("primary\n"), After: []byte("worker\n"),
	})
	history, err := store.History.QueryFileVersions(ctx, "p1", fileID, 20, 0)
	testutil.FailErr(t, "list file versions", err)
	// Newest first: the worker's write, the pre-image the worker branch
	// started from, and the primary create.
	if len(history.Versions) != 3 {
		t.Fatalf("versions = %+v", history.Versions)
	}
	worker := history.Versions[0]
	if !worker.BranchID.IsWorker() ||
		worker.JobID != "job-1" || worker.DerivedFromVersionID != primaryVersionID {
		t.Fatalf("worker version provenance = %+v", worker)
	}
	branchPoint := history.Versions[1]
	if !branchPoint.BranchID.IsWorker() ||
		branchPoint.DerivedFromVersionID != primaryVersionID {
		t.Fatalf("branch-point provenance = %+v", branchPoint)
	}
	if branchPoint.Op != "" || branchPoint.Origin != "" || branchPoint.EffectID != "" {
		t.Fatalf("branch point claims an action it never had = %+v", branchPoint)
	}
	if branchPoint.CaptureState != "stored" {
		t.Fatalf("branch point capture = %q want stored", branchPoint.CaptureState)
	}
	if primary := history.Versions[2]; primary.ID != primaryVersionID ||
		primary.BranchID != sourcebranch.Trunk {
		t.Fatalf("oldest version = %+v want the primary create", primary)
	}
	primaryFileID, _ := mustResolve(t, store, ctx, "main.go")
	if primaryFileID != fileID {
		t.Fatal("worker branch changed the primary file identity")
	}
}

func TestReadRestorableVersionReturnsExactBytesAndAbsence(t *testing.T) {
	store, ctx := openLedger(t)
	raw := []byte{0xff, 0x00, 0x81, 0x7f}
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "asset.bin",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "create-binary", After: raw,
	})
	fileID, contentVersionID := mustResolve(t, store, ctx, "asset.bin")
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "asset.bin", FileID: fileID,
		Op: api.SourceChangeOpDelete, Origin: api.SourceChangeOriginUser,
		OperationID: "delete-binary", Before: raw,
	})
	history, err := store.History.QueryFileVersions(ctx, "p1", fileID, 10, 0)
	testutil.FailErr(t, "query deleted history", err)
	if len(history.Versions) == 0 {
		t.Fatal("deleted history is empty")
	}
	absentVersionID := history.Versions[0].ID

	content, err := store.History.ReadRestorableVersion(ctx, "p1", contentVersionID)
	testutil.FailErr(t, "read content version", err)
	if content.FileID != fileID || content.State != "content" || string(content.Content) != string(raw) {
		t.Fatalf("restorable content = %+v bytes=%x", content, content.Content)
	}
	absent, err := store.History.ReadRestorableVersion(ctx, "p1", absentVersionID)
	testutil.FailErr(t, "read absent version", err)
	if absent.FileID != fileID || absent.State != "absent" || absent.Content != nil {
		t.Fatalf("restorable absence = %+v", absent)
	}
	if _, err := store.History.ReadRestorableVersion(ctx, "another-project", contentVersionID); !errors.Is(err, ErrHistoryNotFound) {
		t.Fatalf("cross-project read error = %v", err)
	}
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "uncaptured.bin",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginExternal,
		OperationID: "create-uncaptured", AfterSHA256: strings.Repeat("a", 64), AfterSize: 12,
	})
	_, unavailableVersionID := mustResolve(t, store, ctx, "uncaptured.bin")
	if _, err := store.History.ReadRestorableVersion(ctx, "p1", unavailableVersionID); !errors.Is(err, ErrVersionUnavailable) {
		t.Fatalf("uncaptured read error = %v", err)
	}
	other := []byte("different retained bytes")
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "other.bin",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "create-other", After: other,
	})
	otherObject, err := store.queries.GetSourceBlobObject(ctx, sourceblob.ContentSHA(other))
	testutil.FailErr(t, "read other source object", err)
	_, err = store.sqlDB.ExecContext(ctx,
		`UPDATE source_blob_objects SET storage_relpath = storage_relpath || '.unmapped' WHERE sha256 = ?`,
		sourceblob.ContentSHA(other),
	)
	testutil.FailErr(t, "unmap other source object", err)
	_, err = store.sqlDB.ExecContext(ctx,
		`UPDATE source_blob_objects SET storage_relpath = ? WHERE sha256 = ?`,
		otherObject.StorageRelpath, sourceblob.ContentSHA(raw),
	)
	testutil.FailErr(t, "redirect source object", err)
	if _, err := store.History.ReadRestorableVersion(ctx, "p1", contentVersionID); !errors.Is(err, ErrVersionUnavailable) {
		t.Fatalf("digest mismatch error = %v", err)
	}
}

func TestRecordBatchStoresOneOperationAndOrderedEffects(t *testing.T) {
	store, ctx := openLedger(t)
	inputs := []RecordInput{
		{ProjectID: "p1", RootID: "r1", Path: "a.txt", Op: api.SourceChangeOpCreate,
			Origin: api.SourceChangeOriginAgent, OperationID: "batch-1", After: []byte("a"),
			SessionID: "session-1", ToolCallID: "call-1", ToolName: "edit"},
		{ProjectID: "p1", RootID: "r1", Path: "b.txt", Op: api.SourceChangeOpCreate,
			Origin: api.SourceChangeOriginAgent, OperationID: "batch-1", After: []byte("b"),
			SessionID: "session-1", ToolCallID: "call-1", ToolName: "edit"},
	}
	testutil.FailErr(t, "record batch", store.RecordBatch(ctx, inputs))
	testutil.FailErr(t, "replay batch", store.RecordBatch(ctx, inputs))
	var operations, effects int
	testutil.FailErr(t, "count operations", store.sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM source_operations WHERE operation_key = 'batch-1'`).Scan(&operations))
	testutil.FailErr(t, "count effects", store.sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM source_effects`).Scan(&effects))
	if operations != 1 || effects != 2 {
		t.Fatalf("operations/effects = %d/%d", operations, effects)
	}
	walk, err := store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "session-1"}, 10, 0, CommitLens{})
	testutil.FailErr(t, "query attributed walk", err)
	if len(walk.Files) != 2 || walk.Files[0].Effects[0].ToolName != "edit" {
		t.Fatalf("walk tool projection = %+v", walk.Files)
	}
}

func TestMaintenancePreservesReferencedVersionContent(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "a.txt",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		Before: []byte("before payload\n"), After: []byte("after payload\n"),
	})
	effectID := latestEffectID(t, store, ctx)
	available, err := store.Comparisons.CompareEffect(ctx, "p1", effectID)
	testutil.FailErr(t, "compare available effect", err)
	_, err = store.sqlDB.ExecContext(ctx, `
		INSERT INTO file_briefings (
			project_id, root_id, path, target_key, attempt_id, presentation, source_sha256,
			trigger, status, preview_json, locations_json, sections_json,
			truncated, error, updated_at, last_accessed_at_ms
		) VALUES ('p1', 'r1', 'a.txt', 'version-summary', '00000000-0000-0000-0000-000000000001', 'version', ?,
			'automatic', 'complete', '{"line_count":1}', '[]', '[]', 0, '', ?, ?)
	`, available.Before.SHA256, db.FormatTime(time.Now()), time.Now().UnixMilli())
	testutil.FailErr(t, "insert version briefing", err)
	testutil.FailErr(t, "maintain blobs", store.Retention.MaintainBlobs(ctx))
	comparison, err := store.Comparisons.CompareEffect(ctx, "p1", effectID)
	testutil.FailErr(t, "compare retained effect", err)
	if comparison.Before.Availability != ContentAvailable || comparison.After.Availability != ContentAvailable {
		t.Fatalf("availability = %s/%s", comparison.Before.Availability, comparison.After.Availability)
	}
	var versions, effects int
	testutil.FailErr(t, "count versions", store.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_versions`).Scan(&versions))
	testutil.FailErr(t, "count effects", store.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_effects`).Scan(&effects))
	if versions != 2 || effects != 1 {
		t.Fatalf("metadata was compacted: versions/effects = %d/%d", versions, effects)
	}
	var briefings int
	testutil.FailErr(t, "count version briefings", store.sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM file_briefings WHERE presentation = 'version'`).Scan(&briefings))
	if briefings != 1 {
		t.Fatalf("version briefings lost while source history remained referenced: %d", briefings)
	}
}

// Snapshot entries can read from the tree without pinning revision objects.
func TestMaintenanceReclaimsObjectsNoHistoryNames(t *testing.T) {
	store, ctx := openLedger(t)
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	testutil.FailErr(t, "write snapshot source", os.WriteFile(path, []byte("package main\n"), 0o600))
	snapshot, err := store.Snapshots.Ensure(ctx, sourcesnapshot.Request{
		Roots: []sourcesnapshot.Root{{Path: root}},
	})
	testutil.FailErr(t, "publish snapshot", err)
	extra := []byte("unprotected content\n")
	extraSHA := sourceblob.ContentSHA(extra)
	extraRel, extraStored, extraOIDs, err := store.Content.Put(extraSHA, extra)
	testutil.FailErr(t, "store unprotected object", err)
	testutil.FailErr(t, "index unprotected object", store.queries.UpsertSourceBlobObject(ctx, db.UpsertSourceBlobObjectParams{
		Sha256: extraSHA, Size: int64(len(extra)), StoredSize: extraStored,
		StorageRelpath: extraRel,
		GitOidSha1:     extraOIDs.SHA1, GitOidSha256: extraOIDs.SHA256,
	}))

	testutil.FailErr(t, "maintain blobs", store.Retention.MaintainBlobs(ctx))
	entry, ok, err := store.Snapshots.Lookup(ctx, snapshot.ID, root, "main.go")
	testutil.FailErr(t, "look up snapshot entry", err)
	if !ok {
		t.Fatal("generation lost its entry")
	}
	raw, err := store.Snapshots.Bytes(ctx, entry)
	testutil.FailErr(t, "read snapshot after maintenance", err)
	if string(raw) != "package main\n" {
		t.Fatalf("snapshot content = %q", raw)
	}
	if _, err := store.queries.GetSourceBlobObject(ctx, extraSHA); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unprotected object error = %v, want sql.ErrNoRows", err)
	}
	if got := countBlobObjects(t, store, ctx); got != 0 {
		t.Fatalf("blob objects after maintenance = %d; a generation copies nothing", got)
	}
}

func countBlobObjects(t *testing.T, store *Store, ctx context.Context) int {
	t.Helper()
	var count int
	testutil.FailErr(t, "count blob objects", store.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_blob_objects`).Scan(&count))
	return count
}

func TestMaintenanceReclaimsContentAfterProjectDeletion(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "deleted.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
		After: []byte("retained until project deletion\n"),
	})
	var sha string
	testutil.FailErr(t, "load retained hash", store.sqlDB.QueryRowContext(ctx,
		`SELECT content_sha256 FROM source_versions WHERE path = 'deleted.txt'`).Scan(&sha))
	object, err := store.queries.GetSourceBlobObject(ctx, sha)
	testutil.FailErr(t, "load retained object", err)
	var queued int
	testutil.FailErr(t, "count initial candidates", store.sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM source_blob_reclaim_queue`).Scan(&queued))
	if queued != 0 {
		t.Fatalf("initial reclaim candidates = %d", queued)
	}
	_, err = store.sqlDB.ExecContext(ctx, `DELETE FROM projects WHERE id = 'p1'`)
	testutil.FailErr(t, "delete project", err)
	testutil.FailErr(t, "count deleted candidates", store.sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM source_blob_reclaim_queue`).Scan(&queued))
	if queued != 1 {
		t.Fatalf("deleted reclaim candidates = %d", queued)
	}
	testutil.FailErr(t, "maintain blobs", store.Retention.MaintainBlobs(ctx))
	if _, err := store.queries.GetSourceBlobObject(ctx, sha); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted project object error = %v, want sql.ErrNoRows", err)
	}
	if exists, err := store.Content.Exists(object.StorageRelpath); err != nil || exists {
		t.Fatalf("deleted project bytes: exists=%v err=%v", exists, err)
	}
}

func TestRecordTxRollsBackHistoryWithCaller(t *testing.T) {
	store, ctx := openLedger(t)
	tx, err := store.sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin transaction", err)
	err = store.RecordTx(ctx, tx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "a.txt", Op: api.SourceChangeOpCreate,
		Origin: api.SourceChangeOriginUser, After: []byte("a"),
	})
	testutil.FailErr(t, "record transaction", err)
	testutil.FailErr(t, "rollback transaction", tx.Rollback())
	var count int
	testutil.FailErr(t, "count effects", store.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_effects`).Scan(&count))
	if count != 0 {
		t.Fatalf("rolled-back effects = %d", count)
	}
}

func TestColdTrackingBoundaryDoesNotMaterializeInventoryAsHistory(t *testing.T) {
	store, ctx := openLedger(t)
	testutil.FailErr(t, "seed tracking boundary", store.Inventory.seedTrackingBoundary(ctx, "p1"))
	var files, versions, effects int
	testutil.FailErr(t, "count files", store.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_files`).Scan(&files))
	testutil.FailErr(t, "count versions", store.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_versions`).Scan(&versions))
	testutil.FailErr(t, "count effects", store.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_effects`).Scan(&effects))
	if files != 0 || versions != 0 || effects != 0 {
		t.Fatalf("cold inventory became history: %d/%d/%d", files, versions, effects)
	}
}

func TestMissingFileIdentityIsNotAPathFallback(t *testing.T) {
	store, ctx := openLedger(t)
	_, _, err := store.History.ResolveFile(ctx, "p1", sourcebranch.Trunk, "r1", "missing.txt")
	if !errors.Is(err, ErrHistoryNotFound) {
		t.Fatalf("resolve missing error = %v", err)
	}
}
