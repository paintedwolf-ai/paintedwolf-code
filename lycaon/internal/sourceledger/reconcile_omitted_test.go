package sourceledger

import (
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestInventoryOmissionDoesNotEstablishTrackedFileDeletion(t *testing.T) {
	for _, rel := range []string{".paintedwolf/blueprints/plan.md", ".paintedwolf-dev/blueprints/plan.md", "ignored/output.txt", "beyond-budget/source.txt"} {
		t.Run(rel, func(t *testing.T) {
			store, ctx, root := openLedgerOnDisk(t)
			// This immutable publication cannot name the explicitly tracked file.
			snapshot, err := store.snapshots.Ensure(ctx, sourcesnapshot.Request{Roots: snapshotRoots(onDiskRoots(root))})
			testutil.FailErr(t, "capture omitted-file manifest", err)
			trackRootFile(t, store, ctx, root, rel, "before\n")
			testutil.FailErr(t, "seed tracking boundary", store.seedTrackingBoundary(ctx, "p1"))
			head, err := store.queries.GetSourceBranchHeadByPath(ctx, db.GetSourceBranchHeadByPathParams{ProjectID: "p1", BranchID: sourcebranch.Trunk.String(), RootID: "r1", Path: rel})
			testutil.FailErr(t, "read tracked head", err)
			for _, changed := range []bool{false, true} {
				if changed {
					writeRootFile(t, root, rel, "after\n")
				}
				out, err := store.reconcileSnapshot(ctx, "p1", snapshot, onDiskRoots(root), nil, nil)
				testutil.FailErr(t, "reconcile omitted file", err)
				want := 0
				if changed {
					want = 1
				}
				if out.recorded != want {
					t.Fatalf("changed=%v recorded=%d, want %d", changed, out.recorded, want)
				}
				current, err := store.queries.GetSourceBranchHeadByPath(ctx, db.GetSourceBranchHeadByPathParams{ProjectID: "p1", BranchID: sourcebranch.Trunk.String(), RootID: "r1", Path: rel})
				testutil.FailErr(t, "read reconciled head", err)
				if current.FileID != head.FileID || current.State != "content" {
					t.Fatalf("omission replaced or deleted tracked identity: %+v", current)
				}
			}
			testutil.FailErr(t, "delete tracked file", os.Remove(filepath.Join(root, filepath.FromSlash(rel))))
			out, err := store.reconcileSnapshot(ctx, "p1", snapshot, onDiskRoots(root), nil, nil)
			testutil.FailErr(t, "reconcile true deletion", err)
			deleted, err := store.queries.GetSourceBranchHeadByFile(ctx, db.GetSourceBranchHeadByFileParams{ProjectID: "p1", BranchID: sourcebranch.Trunk.String(), FileID: head.FileID})
			testutil.FailErr(t, "read deleted identity", err)
			if out.recorded != 1 || deleted.State != "absent" {
				t.Fatalf("direct deletion recorded=%d head=%+v", out.recorded, deleted)
			}
		})
	}
}

// A manifest captured before a recorded write cannot establish that the file
// reverted; only the live file can.
func TestStaleInventoryManifestDoesNotRevertNewerHead(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	writeRootFile(t, root, "a.txt", "original\n")
	snapshot, err := store.snapshots.Ensure(ctx, sourcesnapshot.Request{Roots: snapshotRoots(onDiskRoots(root))})
	testutil.FailErr(t, "capture manifest before the write", err)
	trackRootFile(t, store, ctx, root, "a.txt", "edited\n")
	testutil.FailErr(t, "seed tracking boundary", store.seedTrackingBoundary(ctx, "p1"))
	head, err := store.queries.GetSourceBranchHeadByPath(ctx, db.GetSourceBranchHeadByPathParams{ProjectID: "p1", BranchID: sourcebranch.Trunk.String(), RootID: "r1", Path: "a.txt"})
	testutil.FailErr(t, "read recorded head", err)

	out, err := store.reconcileSnapshot(ctx, "p1", snapshot, onDiskRoots(root), nil, nil)
	testutil.FailErr(t, "reconcile stale manifest", err)
	current, err := store.queries.GetSourceBranchHeadByFile(ctx, db.GetSourceBranchHeadByFileParams{ProjectID: "p1", BranchID: sourcebranch.Trunk.String(), FileID: head.FileID})
	testutil.FailErr(t, "read head after stale reconcile", err)
	if out.recorded != 0 || current.VersionID != head.VersionID {
		t.Fatalf("stale manifest recorded=%d head=%+v, want the recorded write kept", out.recorded, current)
	}

	writeRootFile(t, root, "a.txt", "outside\n")
	out, err = store.reconcileSnapshot(ctx, "p1", snapshot, onDiskRoots(root), nil, nil)
	testutil.FailErr(t, "reconcile outside edit", err)
	current, err = store.queries.GetSourceBranchHeadByFile(ctx, db.GetSourceBranchHeadByFileParams{ProjectID: "p1", BranchID: sourcebranch.Trunk.String(), FileID: head.FileID})
	testutil.FailErr(t, "read head after outside edit", err)
	if out.recorded != 1 || current.ContentSha256 != sourceblob.ContentSHA([]byte("outside\n")) {
		t.Fatalf("outside edit recorded=%d head=%+v, want the live bytes", out.recorded, current)
	}
}

func TestInventoryDefersAttributionUntilSourceMutationCommits(t *testing.T) {
	for _, status := range []string{"prepared", "file_applied"} {
		t.Run(status, func(t *testing.T) {
			store, ctx, root := openLedgerOnDisk(t)
			trackRootFile(t, store, ctx, root, "a.txt", "before\n")
			testutil.FailErr(t, "seed tracking", store.seedTrackingBoundary(ctx, "p1"))
			snapshot, err := store.snapshots.Ensure(ctx, sourcesnapshot.Request{Roots: snapshotRoots(onDiskRoots(root))})
			testutil.FailErr(t, "capture inventory", err)
			_, err = store.sqlDB.ExecContext(ctx, `INSERT INTO source_mutations(id,project_id,kind,input_digest,plan_json,status,created_at,updated_at) VALUES('pending','p1','write','input','{}',?,'2026-10-08T00:00:00Z','2026-10-08T00:00:00Z')`, status)
			testutil.FailErr(t, "prepare durable mutation", err)
			writeRootFile(t, root, "a.txt", "in app\n")
			out, err := store.reconcileSnapshot(ctx, "p1", snapshot, onDiskRoots(root), nil, nil)
			testutil.FailErr(t, "reconcile published bytes before attribution", err)
			if out.recorded != 0 {
				t.Fatal("in flight mutation was attributed outside the app")
			}
			testutil.FailErr(t, "commit mutation attribution", store.Record(ctx, RecordInput{ProjectID: "p1", RootID: "r1", Path: "a.txt", Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser, OperationID: "pending", Before: []byte("before\n"), After: []byte("in app\n")}))
			_, err = store.sqlDB.ExecContext(ctx, `UPDATE source_mutations SET status='committed' WHERE id='pending'`)
			testutil.FailErr(t, "finish durable mutation", err)
			out, err = store.reconcileSnapshot(ctx, "p1", snapshot, onDiskRoots(root), nil, nil)
			testutil.FailErr(t, "reconcile committed write", err)
			if out.recorded != 0 {
				t.Fatal("committed write was attributed a second time")
			}
			writeRootFile(t, root, "a.txt", "outside\n")
			out, err = store.reconcileSnapshot(ctx, "p1", snapshot, onDiskRoots(root), nil, nil)
			testutil.FailErr(t, "reconcile later outside write", err)
			if out.recorded != 1 {
				t.Fatal("resolved mutation kept outside attribution blocked")
			}
		})
	}
}
