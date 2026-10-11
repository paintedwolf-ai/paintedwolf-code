package sourceledger

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func seedRecoveryManifest(t *testing.T, s *Store, id string, size int) {
	t.Helper()
	tx, err := s.sqlDB.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin manifest", err)
	defer func() { _ = tx.Rollback() }()
	for i := 0; i < size; i++ {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO source_recovery_entries(project_id,recovery_id,ordinal,path,mode) VALUES('p1',?,?,?,448)`, id, i, fmt.Sprint(i))
		testutil.FailErr(t, "insert manifest entry", err)
	}
	testutil.FailErr(t, "commit manifest", tx.Commit())
}

func TestRecoveryReclamationBoundsEachWriterTransaction(t *testing.T) {
	s, ctx := openLedger(t)
	seedRecoveryManifest(t, s, "orphan", blobMaintenanceBatchSize*3+1)
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin unrelated history", err)
	for i := 0; i < 1000; i++ {
		_, err := tx.ExecContext(ctx, `INSERT INTO source_mutations(id,project_id,kind,input_digest,plan_json,status,created_at,updated_at) VALUES(?,'p1','delete','digest','{}','committed','','')`, fmt.Sprint(i))
		testutil.FailErr(t, "insert unrelated receipt", err)
	}
	testutil.FailErr(t, "commit unrelated history", tx.Commit())
	for pass := 0; pass < 4; pass++ {
		n, err := s.Retention.reclaimRecoveryBatch(ctx)
		testutil.FailErr(t, "reclaim recovery page", err)
		want := int64(blobMaintenanceBatchSize)
		if pass == 3 {
			want = 1
		}
		if n != want {
			t.Fatalf("pass %d reclaimed %d, want %d", pass, n, want)
		}
	}
	n, err := s.Retention.reclaimRecoveryBatch(ctx)
	testutil.FailErr(t, "retire drained candidate", err)
	if n != 1 {
		t.Fatalf("candidate not retired: %d", n)
	}
	n, err = s.Retention.reclaimRecoveryBatch(ctx)
	testutil.FailErr(t, "repeat maintenance", err)
	if n != 0 {
		t.Fatalf("empty queue did work: %d", n)
	}
}

func TestRecoveryClaimsSurviveUntilLastReferenceReleases(t *testing.T) {
	for _, reference := range []string{"undo", "redo", "prepared", "file_applied", "failed", "diverged", "operation identity"} {
		t.Run(reference, func(t *testing.T) {
			s, ctx := openLedger(t)
			seedRecoveryManifest(t, s, "retained", 1)
			var remove string
			if reference == "undo" || reference == "redo" {
				undo, redo := "{}", "{}"
				if reference == "undo" {
					undo = `{"recovery_id":"retained"}`
				} else {
					redo = `{"recovery_id":"retained"}`
				}
				_, err := s.sqlDB.ExecContext(ctx, `INSERT INTO source_history_entries(id,project_id,kind,state,undo_label,redo_label,undo_plan_json,redo_plan_json,created_at,updated_at) VALUES('history','p1','trash','applied','','',?,?,'','')`, undo, redo)
				testutil.FailErr(t, "claim history", err)
				remove = `DELETE FROM source_history_entries WHERE id='history'`
			} else {
				id, status, plan := "receipt", reference, `{"recovery_id":"retained"}`
				if reference == "operation identity" {
					id, status, plan = "retained", "prepared", ""
				}
				_, err := s.sqlDB.ExecContext(ctx, `INSERT INTO source_mutations(id,project_id,kind,input_digest,plan_json,status,created_at,updated_at) VALUES(?,'p1','delete','digest',?,?,'','')`, id, plan, status)
				testutil.FailErr(t, "claim mutation", err)
				remove = `UPDATE source_mutations SET status='committed' WHERE project_id='p1'`
			}
			n, err := s.Retention.reclaimRecoveryBatch(ctx)
			testutil.FailErr(t, "dequeue retained manifest", err)
			if n != 1 {
				t.Fatalf("retained candidate work=%d", n)
			}
			var count int
			testutil.FailErr(t, "count retained manifest", s.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_recovery_entries`).Scan(&count))
			if count != 1 {
				t.Fatal("claimed manifest removed")
			}
			tx, err := s.sqlDB.BeginTx(ctx, nil)
			testutil.FailErr(t, "begin rollback", err)
			_, err = tx.ExecContext(ctx, remove)
			testutil.FailErr(t, "release tentative reference", err)
			testutil.FailErr(t, "rollback release", tx.Rollback())
			n, err = s.Retention.reclaimRecoveryBatch(ctx)
			testutil.FailErr(t, "inspect rolled-back release", err)
			if n != 0 {
				t.Fatal("rolled-back claim release queued reclamation")
			}
			_, err = s.sqlDB.ExecContext(ctx, remove)
			testutil.FailErr(t, "release reference", err)
			testutil.FailErr(t, "drain background collector", s.Retention.SweepBlobs(ctx))
			testutil.FailErr(t, "count released manifest", s.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_recovery_entries`).Scan(&count))
			if count != 0 {
				t.Fatal("released manifest retained")
			}
		})
	}
}

func TestRecoveryClaimLookupsUseExactIdentityIndexes(t *testing.T) {
	s, ctx := openLedger(t)
	rows, err := s.sqlDB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+recoveryClaimQuery, "p1", "recovery")
	testutil.FailErr(t, "explain recovery references", err)
	defer rows.Close()
	lookups := 0
	for rows.Next() {
		var id, parent, unused int
		var detail string
		testutil.FailErr(t, "read query plan", rows.Scan(&id, &parent, &unused, &detail))
		if strings.Contains(detail, "source_history_entries") || strings.Contains(detail, "source_mutations") {
			if !strings.HasPrefix(detail, "SEARCH ") {
				t.Fatalf("references scans unrelated history: %s", detail)
			}
			if !strings.Contains(detail, "<expr>=?") && !strings.Contains(detail, "(id=?)") {
				t.Fatalf("references lacks exact recovery lookup: %s", detail)
			}
			lookups++
		}
	}
	testutil.FailErr(t, "finish query plan", rows.Err())
	if lookups != 4 {
		t.Fatalf("references lookups=%d, want 4", lookups)
	}
}
