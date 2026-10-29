package sourcesnapshot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPublicationDoesNotRunMaintenance(t *testing.T) {
	s := openSnapshotStore(t)
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO source_manifest_staging
 (build_id,path,identity,size,mode) VALUES ('previous-process','orphan','stat',0,0)`)
	testutil.FailErr(t, "seed interrupted build", err)
	_, err = s.db.ExecContext(t.Context(), `CREATE TRIGGER refuse_staging_cleanup BEFORE DELETE ON source_manifest_staging
 BEGIN SELECT RAISE(ABORT, 'maintenance fixture'); END`)
	testutil.FailErr(t, "install maintenance fault", err)
	snapshot, err := s.Ensure(t.Context(), Request{Roots: []Root{{Path: t.TempDir()}}})
	testutil.FailErr(t, "publish empty root", err)
	if snapshot.ID == "" || snapshot.FileCount != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM source_manifest_staging`); got != 1 {
		t.Fatalf("publication reclaimed %d staging rows", 1-got)
	}
	if err := s.Sweep(t.Context(), time.Now().Add(-Retention)); err == nil {
		t.Fatal("maintenance fault was not exercised")
	}
}

func TestSweepAdmissionAndCancellation(t *testing.T) {
	s := openSnapshotStore(t)
	s.sweepMu.Lock()
	if err := s.Sweep(t.Context(), time.Now()); !errors.Is(err, ErrSweepInProgress) {
		t.Fatalf("concurrent sweep = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Sweep(ctx, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sweep = %v", err)
	}
	s.sweepMu.Unlock()
	var absent *Store
	testutil.FailErr(t, "absent store", absent.Sweep(t.Context(), time.Now()))
}

func TestSweepCandidatePagesIncludeRetainedRows(t *testing.T) {
	s := openSnapshotStore(t)
	ctx := t.Context()
	old := db.FormatTime(time.Now().Add(-2 * Retention))
	now := db.FormatTime(time.Now())
	_, err := s.db.ExecContext(ctx, `INSERT INTO source_snapshots
 (id,roots_key,merkle_sha256,file_count,total_bytes,created_ts) VALUES ('a','root','a',0,0,?)`, old)
	testutil.FailErr(t, "seed retained snapshot", err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO source_snapshot_heads (roots_key,snapshot_id,published_ts) VALUES ('root','a',?)`, now)
	testutil.FailErr(t, "retain snapshot", err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO source_manifests (id,entry_count,total_bytes) VALUES ('a',1,0),('z',1,0)`)
	testutil.FailErr(t, "seed manifests", err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO source_snapshot_roots (snapshot_id,root_path,ordinal,manifest_id) VALUES ('a','/root',0,'a')`)
	testutil.FailErr(t, "retain manifest", err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO source_manifest_chunks (id,content_id,entry_count,total_bytes,hashed_count) VALUES ('a','a',1,0,0),('z','z',1,0,0)`)
	testutil.FailErr(t, "seed chunks", err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO source_manifest_layout (manifest_id,first_path,chunk_id) VALUES ('a','file','a')`)
	testutil.FailErr(t, "retain chunk", err)
	manifests, err := s.queries.ListSourceManifestSweepCandidates(ctx, db.ListSourceManifestSweepCandidatesParams{BatchLimit: 1})
	testutil.FailErr(t, "manifest candidate page", err)
	if len(manifests) != 1 || manifests[0] != "a" {
		t.Fatalf("candidate scan skipped retained row: %v", manifests)
	}
	chunks, err := s.queries.ListSourceChunkSweepCandidates(ctx, db.ListSourceChunkSweepCandidatesParams{BatchLimit: 1})
	testutil.FailErr(t, "chunk candidate page", err)
	if len(chunks) != 1 || chunks[0].ID != "a" {
		t.Fatalf("candidate scan skipped retained chunk: %v", chunks)
	}
	snapshots, err := s.queries.ListSourceSnapshotSweepCandidates(ctx, db.ListSourceSnapshotSweepCandidatesParams{CreatedBefore: now, BatchLimit: 1})
	testutil.FailErr(t, "snapshot candidate page", err)
	if len(snapshots) != 1 || snapshots[0].ID != "a" {
		t.Fatalf("candidate scan skipped retained snapshot: %v", snapshots)
	}
	testutil.FailErr(t, "sweep", s.Sweep(ctx, time.Now().Add(-Retention)))
	for _, table := range []string{"source_snapshots", "source_manifests", "source_manifest_chunks"} {
		if got := countRows(t, s, "SELECT COUNT(*) FROM "+table); got != 1 {
			t.Fatalf("%s retained rows = %d", table, got)
		}
	}
}

func TestSweepStagingKeepsActiveProcessAcrossPages(t *testing.T) {
	s := openSnapshotStore(t)
	ctx := t.Context()
	_, err := s.db.ExecContext(ctx, `WITH RECURSIVE entries(n) AS (
 SELECT 1 UNION ALL SELECT n+1 FROM entries WHERE n < ?)
 INSERT INTO source_manifest_staging (build_id,path,identity,size,mode)
 SELECT 'previous-process',printf('%06d',n),'stat',0,0 FROM entries`, 2*stagingPage+1)
	testutil.FailErr(t, "seed abandoned pages", err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO source_manifest_staging (build_id,path,identity,size,mode)
 VALUES (?, 'active', 'stat', 0, 0)`, newBuildID())
	testutil.FailErr(t, "seed active build", err)
	testutil.FailErr(t, "sweep staging", s.Sweep(ctx, time.Now()))
	if got := countRows(t, s, `SELECT COUNT(*) FROM source_manifest_staging`); got != 1 {
		t.Fatalf("staging rows = %d, want active build only", got)
	}
	var path string
	testutil.FailErr(t, "read retained staging", s.db.QueryRowContext(ctx, `SELECT path FROM source_manifest_staging`).Scan(&path))
	if path != "active" {
		t.Fatalf("retained staging = %q", path)
	}
}
