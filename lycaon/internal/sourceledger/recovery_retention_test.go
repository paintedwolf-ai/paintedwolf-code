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
			want = 2 // the final manifest row and its now-empty candidate
		}
		if n != want {
			t.Fatalf("pass %d reclaimed %d, want %d", pass, n, want)
		}
	}
	n, err := s.Retention.reclaimRecoveryBatch(ctx)
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

func TestRecoveryReclamationBatchesEmptyCandidatesWithinTheSameBound(t *testing.T) {
	s, ctx := openLedger(t)
	const total = 2*blobMaintenanceBatchSize + 1
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin candidate fixture", err)
	defer func() { _ = tx.Rollback() }()
	for i := range total {
		_, err := tx.ExecContext(ctx, `INSERT INTO source_recovery_reclaim_queue(project_id,recovery_id) VALUES('p1',?)`, fmt.Sprintf("empty-%04d", i))
		testutil.FailErr(t, "queue empty recovery candidate", err)
	}
	testutil.FailErr(t, "commit candidate fixture", tx.Commit())
	remaining := total
	for _, want := range []int64{blobMaintenanceBatchSize, blobMaintenanceBatchSize, 1, 0} {
		n, err := s.Retention.reclaimRecoveryBatch(ctx)
		testutil.FailErr(t, "retire bounded candidates", err)
		if n != want {
			t.Fatalf("candidate work=%d want=%d", n, want)
		}
		remaining -= int(n)
		var actual int
		testutil.FailErr(t, "count retained candidates", s.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_recovery_reclaim_queue`).Scan(&actual))
		if actual != remaining {
			t.Fatalf("remaining candidates=%d want=%d", actual, remaining)
		}
	}
}

func TestRecoveryEmptyCandidateBatchDoesNotPassItsSelectedWindow(t *testing.T) {
	s, ctx := openLedger(t)
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin claimed candidate fixture", err)
	defer func() { _ = tx.Rollback() }()
	for i := range blobMaintenanceBatchSize {
		id := fmt.Sprintf("a-claimed-%04d", i)
		_, err := tx.ExecContext(ctx, `INSERT INTO source_recovery_entries(project_id,recovery_id,ordinal,path,mode) VALUES('p1',?,0,'file',448)`, id)
		testutil.FailErr(t, "seed retained manifest", err)
		_, err = tx.ExecContext(ctx, `INSERT INTO source_mutations(id,project_id,kind,input_digest,plan_json,status,created_at,updated_at) VALUES(?,'p1','delete','digest','','prepared','','')`, id)
		testutil.FailErr(t, "claim actual operation identity", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO source_recovery_reclaim_queue(project_id,recovery_id) VALUES('p1','z-empty')`)
	testutil.FailErr(t, "seed candidate outside first window", err)
	testutil.FailErr(t, "commit claimed fixture", tx.Commit())
	_, err = s.sqlDB.ExecContext(ctx, `CREATE TRIGGER refuse_unselected_candidate BEFORE DELETE ON source_recovery_reclaim_queue WHEN OLD.recovery_id='z-empty' BEGIN SELECT RAISE(ABORT,'outside selected window'); END`)
	testutil.FailErr(t, "guard later candidate", err)
	n, err := s.Retention.reclaimRecoveryBatch(ctx)
	testutil.FailErr(t, "retire only first candidate window", err)
	if n != blobMaintenanceBatchSize {
		t.Fatalf("selected candidate work=%d", n)
	}
	var manifests, queued int
	testutil.FailErr(t, "count live manifests", s.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_recovery_entries`).Scan(&manifests))
	testutil.FailErr(t, "count later candidate", s.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_recovery_reclaim_queue WHERE recovery_id='z-empty'`).Scan(&queued))
	if manifests != blobMaintenanceBatchSize || queued != 1 {
		t.Fatalf("live manifests=%d later candidate=%d", manifests, queued)
	}
	_, err = s.sqlDB.ExecContext(ctx, `DROP TRIGGER refuse_unselected_candidate`)
	testutil.FailErr(t, "release later candidate guard", err)
	n, err = s.Retention.reclaimRecoveryBatch(ctx)
	testutil.FailErr(t, "retire next actual window", err)
	if n != 1 {
		t.Fatalf("later candidate work=%d", n)
	}
}
