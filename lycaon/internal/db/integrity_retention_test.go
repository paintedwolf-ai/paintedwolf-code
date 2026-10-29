package db

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPendingAuditDefersDeletionButKeepsSpaceReclamation(t *testing.T) {
	_, store := uncleanAuditStore(t)
	seedCommandInvocationRow(t, store, "pending-audit-receipt", "2000-01-01T00:00:00.000000000Z")
	_, err := store.ExecContext(t.Context(), `CREATE TABLE audit_free_pages(body BLOB);
 INSERT INTO audit_free_pages VALUES(zeroblob(4194304)); DROP TABLE audit_free_pages`)
	testutil.FailErr(t, "seed independently freed pages", err)
	var before, after int64
	testutil.FailErr(t, "read pending freelist", store.QueryRowContext(t.Context(), "PRAGMA freelist_count").Scan(&before))
	if before < DefaultRetention().IncrementalVacuumMinFreelistPages {
		t.Fatalf("fixture has insufficient reusable pages: %d", before)
	}
	report, err := RunRetention(t.Context(), store, DefaultRetention())
	testutil.FailErr(t, "perform resource work during pending audit", err)
	if report.TotalDeleted() != 0 || !report.IncrementalVacuumed || len(report.FTSOptimized) != 0 {
		t.Fatalf("pending audit mixed deletion or index rewriting with resource work: %+v", report)
	}
	testutil.FailErr(t, "read reclaimed pending freelist", store.QueryRowContext(t.Context(), "PRAGMA freelist_count").Scan(&after))
	if after >= before {
		t.Fatalf("pending audit blocked resource reclamation: %d -> %d", before, after)
	}
	var retained int
	testutil.FailErr(t, "read retained old receipt", store.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM command_invocations WHERE operation_id='pending-audit-receipt'").Scan(&retained))
	if retained != 1 {
		t.Fatal("unaudited retention deleted the old receipt")
	}
	_, err = CheckpointWAL(t.Context(), store)
	testutil.FailErr(t, "checkpoint resource work before full audit", err)
	assertAuditPending(t, store, true)
	testutil.FailErr(t, "complete background audit", AuditIntegrity(t.Context(), store))
	report, err = RunRetention(t.Context(), store, DefaultRetention())
	testutil.FailErr(t, "resume audited retention", err)
	if report.JournalDeleted("command_invocations") != 1 {
		t.Fatalf("audit completion did not release receipt cleanup: %+v", report)
	}
}
