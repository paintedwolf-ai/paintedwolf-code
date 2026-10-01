package sourceledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// agedOrphan plants an object no row names, old enough for a pass to judge.
func agedOrphan(t *testing.T, store *Store, content string) string {
	t.Helper()
	rel, _, _, err := store.objects.Put(sourceblob.ContentSHA([]byte(content)), []byte(content))
	testutil.FailErr(t, "store orphan", err)
	settled := time.Now().Add(-48 * time.Hour)
	testutil.FailErr(t, "age orphan", os.Chtimes(filepath.Join(store.objects.Root(), rel), settled, settled))
	return rel
}

func TestSweepDefersBehindACaptureWithoutHoldingTheLedger(t *testing.T) {
	store, ctx := openLedger(t)
	orphan := agedOrphan(t, store, "written by an interrupted capture\n")
	releaseCapture := store.objects.AcquireReferenceLease()

	started := time.Now()
	err := store.SweepBlobs(ctx)
	if !errors.Is(err, ErrBlobMaintenanceDeferred) {
		t.Fatalf("sweep during a capture = %v, want ErrBlobMaintenanceDeferred", err)
	}
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("a deferred sweep waited %s for the capture", waited)
	}
	if exists, err := store.objects.Exists(orphan); err != nil || !exists {
		t.Fatalf("a deferred sweep removed an object: exists=%v err=%v", exists, err)
	}

	// A turn checkpoint and a record must not wait for the capture.
	done := make(chan error, 1)
	go func() {
		_, err := store.CreateStructuralCheckpoint(ctx, StructuralCheckpointInput{
			ProjectID: "p1", Kind: CheckpointTurn, Label: "Turn start", SessionID: "s1", Turn: 1,
		})
		if err != nil {
			done <- err
			return
		}
		done <- store.Record(ctx, RecordInput{
			ProjectID: "p1", RootID: "r1", Path: "a.txt",
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
			After: []byte("recorded while a capture runs\n"),
		})
	}()
	select {
	case err := <-done:
		testutil.FailErr(t, "checkpoint and record during a capture", err)
	case <-time.After(5 * time.Second):
		t.Fatal("a checkpoint waited behind a capture the sweep was deferred by")
	}

	releaseCapture()
	testutil.FailErr(t, "sweep after the capture", store.SweepBlobs(ctx))
	if exists, err := store.objects.Exists(orphan); err != nil || exists {
		t.Fatalf("orphan survived the sweep: exists=%v err=%v", exists, err)
	}
}

func TestSweepJudgesEveryShardAndDrainsTheQueue(t *testing.T) {
	store, ctx := openLedger(t)
	first := agedOrphan(t, store, "orphan one\n")
	second := agedOrphan(t, store, "orphan two\n")
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "kept.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
		After: []byte("referenced by a version\n"),
	})
	var keptSHA string
	testutil.FailErr(t, "load kept hash", store.sqlDB.QueryRowContext(ctx,
		`SELECT content_sha256 FROM source_versions WHERE path = 'kept.txt'`).Scan(&keptSHA))
	kept, err := store.queries.GetSourceBlobObject(ctx, keptSHA)
	testutil.FailErr(t, "load kept object", err)
	settled := time.Now().Add(-48 * time.Hour)
	testutil.FailErr(t, "age kept object", os.Chtimes(filepath.Join(store.objects.Root(), kept.StorageRelpath), settled, settled))

	testutil.FailErr(t, "sweep", store.SweepBlobs(ctx))
	for _, rel := range []string{first, second} {
		if exists, err := store.objects.Exists(rel); err != nil || exists {
			t.Fatalf("orphan %s survived: exists=%v err=%v", rel, exists, err)
		}
	}
	if exists, err := store.objects.Exists(kept.StorageRelpath); err != nil || !exists {
		t.Fatalf("referenced object removed: exists=%v err=%v", exists, err)
	}
	var queued int
	testutil.FailErr(t, "count queue", store.sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM source_blob_reclaim_queue`).Scan(&queued))
	if queued != 0 {
		t.Fatalf("reclaim queue kept %d settled candidates", queued)
	}
}

func TestMaintenanceWaitsForARecordTransactionToCommit(t *testing.T) {
	store, ctx := openLedger(t)
	content := []byte("recorded through a caller transaction\n")
	sha := sourceblob.ContentSHA(content)
	// An unreferenced, reclaimable object the pending record is about to reuse.
	rel, stored, oids, err := store.objects.Put(sha, content)
	testutil.FailErr(t, "store object", err)
	settled := time.Now().Add(-48 * time.Hour)
	testutil.FailErr(t, "age object", os.Chtimes(filepath.Join(store.objects.Root(), rel), settled, settled))
	testutil.FailErr(t, "index object", store.queries.UpsertSourceBlobObject(ctx, db.UpsertSourceBlobObjectParams{
		Sha256: sha, Size: int64(len(content)), StoredSize: stored, StorageRelpath: rel,
		GitOidSha1: oids.SHA1, GitOidSha256: oids.SHA256,
	}))

	tx, err := store.sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin caller transaction", err)
	testutil.FailErr(t, "record inside caller transaction", store.RecordTx(ctx, tx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "joined.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser, After: content,
	}))

	swept := make(chan error, 1)
	go func() { swept <- store.SweepBlobs(ctx) }()
	select {
	case err := <-swept:
		t.Fatalf("sweep finished (%v) while a record transaction held the writer", err)
	case <-time.After(300 * time.Millisecond):
	}
	testutil.FailErr(t, "commit caller transaction", tx.Commit())
	select {
	case err := <-swept:
		testutil.FailErr(t, "sweep after commit", err)
	case <-time.After(10 * time.Second):
		t.Fatal("sweep never finished after the record committed")
	}
	if exists, err := store.objects.Exists(rel); err != nil || !exists {
		t.Fatalf("object a committed version references was reclaimed: exists=%v err=%v", exists, err)
	}
	if _, err := store.queries.GetSourceBlobObject(ctx, sha); err != nil {
		t.Fatalf("referenced object row lost: %v", err)
	}
}

func TestRunBlobGCRetriesADeferredSweep(t *testing.T) {
	store, ctx := openLedger(t)
	orphan := agedOrphan(t, store, "waits for the capture to end\n")
	releaseCapture := store.objects.AcquireReferenceLease()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- store.RunBlobGC(runCtx, time.Hour, 5*time.Millisecond) }()

	time.Sleep(100 * time.Millisecond)
	if exists, err := store.objects.Exists(orphan); err != nil || !exists {
		t.Fatalf("gc removed an object during a capture: exists=%v err=%v", exists, err)
	}
	releaseCapture()
	deadline := time.Now().Add(10 * time.Second)
	for {
		exists, err := store.objects.Exists(orphan)
		testutil.FailErr(t, "stat orphan", err)
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("gc never retried the sweep it deferred")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-stopped:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("gc stopped with %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gc did not stop with its context")
	}
}

func TestInventoryPassCompletesWhenItsRepairIsDeferred(t *testing.T) {
	store, ctx, root := openLedgerOnDisk(t)
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600))
	releaseCapture := store.objects.AcquireReferenceLease()
	defer releaseCapture()
	testutil.FailErr(t, "inventory during another capture", store.EnsureInventory(ctx, InventoryRequest{
		ProjectID: "p1", Roots: []RootSpec{{ID: "r1", Path: root}}, Force: true, Wait: true,
	}))
	state, err := store.InventoryState(ctx, "p1", "", 0)
	testutil.FailErr(t, "inventory state", err)
	if state.Phase != InventoryReady || state.FileCount != 1 {
		t.Fatalf("inventory = %+v, want ready with one file", state)
	}
}
