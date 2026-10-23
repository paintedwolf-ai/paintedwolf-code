package db

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// The background audit reads one table at a time, so writes during it are
// checkpointed between tables instead of piling up behind one snapshot.
func TestAuditLetsTheWALCheckpointBetweenTables(t *testing.T) {
	store, _ := openTestDBWithPath(t)
	audited, complete := 0, 0
	store.auditedTable = func(table string) {
		audited++
		growWAL(t, store, "audit-"+table, 20)
		result, err := CheckpointWAL(t.Context(), store)
		testutil.FailErr(t, "checkpoint during audit", err)
		if result.Complete() {
			complete++
		}
	}
	testutil.FailErr(t, "audit store", AuditIntegrity(t.Context(), store))
	if audited < 10 {
		t.Fatalf("audit visited %d tables, want every table", audited)
	}
	if complete != audited {
		t.Fatalf("%d of %d checkpoints during the audit were pinned by it", audited-complete, audited)
	}
}
