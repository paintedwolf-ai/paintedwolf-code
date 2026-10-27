package sourceledger

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestInventoryPublishesBeforeUnrelatedBlobMaintenance(t *testing.T) {
	s, ctx := openLedger(t)
	orphan := agedOrphan(t, s, "unrelated interrupted capture")
	testutil.FailErr(t, "publish inventory", s.Inventory.EnsureInventory(ctx, InventoryRequest{
		ProjectID: "p1", RootsGeneration: 1, Roots: []RootSpec{{ID: "r1", Path: t.TempDir()}},
	}))
	state, err := s.Inventory.InventoryState(ctx, "p1", "", 1)
	testutil.FailErr(t, "inventory state", err)
	if !state.Complete || state.Phase != InventoryReady {
		t.Fatalf("inventory = %+v", state)
	}
	exists, err := s.Content.Exists(orphan)
	testutil.FailErr(t, "check deferred orphan", err)
	if !exists {
		t.Fatal("foreground inventory performed unrelated maintenance")
	}
	testutil.FailErr(t, "run maintenance", s.Retention.SweepBlobs(ctx))
	exists, err = s.Content.Exists(orphan)
	testutil.FailErr(t, "check reclaimed orphan", err)
	if exists {
		t.Fatal("maintenance did not reclaim orphan")
	}
}

func TestMaintenanceReportsSnapshotFailure(t *testing.T) {
	s, ctx := openLedger(t)
	_, err := s.sqlDB.ExecContext(ctx, `INSERT INTO source_manifest_staging (build_id,path,identity,size,mode)
 VALUES ('previous-process','orphan','stat',0,0)`)
	testutil.FailErr(t, "seed interrupted build", err)
	_, err = s.sqlDB.ExecContext(ctx, `CREATE TRIGGER refuse_staging_cleanup BEFORE DELETE ON source_manifest_staging
 BEGIN SELECT RAISE(ABORT, 'maintenance fixture'); END`)
	testutil.FailErr(t, "install maintenance fault", err)
	if err := s.Retention.RunBlobGC(ctx, time.Hour, time.Hour); err == nil {
		t.Fatal("maintenance runner hid snapshot failure")
	}
}
