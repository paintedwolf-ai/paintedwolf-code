package sourcesnapshot

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Collection frees pages that the store's own maintenance wake then returns,
// while a held root and an in-flight publication keep every row they need.
func TestCollectionThenReclamationKeepsPublication_AC_DB_01_AC_DB_02_AC_DB_03(t *testing.T) {
	s := openSnapshotStore(t)
	ctx := t.Context()
	database, ok := s.db.(*db.Store)
	if !ok {
		t.Fatalf("store handle is %T", s.db)
	}
	old := db.FormatTime(time.Now().Add(-2 * Retention))
	seedChunk(t, s, "held-chunk", 500)
	seedManifest(t, s, "held", "held-chunk")
	seedSnapshot(t, s, "held", old, "held")
	_, err := s.db.ExecContext(ctx, `INSERT INTO source_snapshot_heads (roots_key,snapshot_id,published_ts) VALUES ('live','held','2999-01-01T00:00:00Z')`)
	testutil.FailErr(t, "publish held root", err)
	seedChunk(t, s, "built-chunk", 500)
	seedManifest(t, s, "building")
	builder := s.pins.holdManifests("building")
	builder.addChunk("built-chunk")
	for i := range 4 {
		id := fmt.Sprintf("gone-%d", i)
		seedChunk(t, s, id, 12000)
		seedManifest(t, s, id, id)
		seedSnapshot(t, s, id, old, id)
	}
	retained := countRows(t, s, `SELECT COUNT(*) FROM source_manifest_entries WHERE chunk_id IN ('held-chunk','built-chunk')`)

	report, _ := sweepCycle(t, s)
	if report.SnapshotsDeleted != 4 || report.ChunksDeleted != 4 || report.OversizedDeferred != 0 {
		t.Fatalf("collection = %+v", report)
	}
	cfg := db.DefaultRetention()
	before, err := database.MaintenanceProgress(ctx, cfg)
	testutil.FailErr(t, "read progress", err)
	if before.FreelistPages < cfg.IncrementalVacuumMinFreelistPages {
		t.Fatalf("collection freed %d pages, fixture too small", before.FreelistPages)
	}

	// Foreground writes wake maintenance; nothing else requests it.
	wake, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- db.RunMaintenance(wake, database, cfg) }()
	var progress db.MaintenanceProgress
	for progress.PagesReclaimed == 0 {
		for range 64 {
			_, err := s.db.ExecContext(ctx, `DELETE FROM source_manifest_entries WHERE chunk_id='no-such-chunk'`)
			testutil.FailErr(t, "foreground write", err)
		}
		progress, err = database.MaintenanceProgress(ctx, cfg)
		testutil.FailErr(t, "read progress", err)
	}
	stop()
	<-done

	if progress.PageCount >= before.PageCount {
		t.Fatalf("file stayed at %d pages after reclaiming %d", progress.PageCount, progress.PagesReclaimed)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM source_manifest_entries WHERE chunk_id IN ('held-chunk','built-chunk')`); got != retained {
		t.Fatalf("retained entries %d, want %d", got, retained)
	}

	builder.release()
	sweepAdmission(t, s)
	if got := countRows(t, s, `SELECT COUNT(*) FROM source_manifest_chunks WHERE id='built-chunk'`); got != 0 {
		t.Fatal("released publication stayed after its pin")
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM source_manifest_chunks WHERE id='held-chunk'`); got != 1 {
		t.Fatal("collection took a chunk a published root still reaches")
	}
}
