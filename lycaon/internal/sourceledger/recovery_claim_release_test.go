package sourceledger

import (
	"github.com/lycaon/lycaon/pkg/api"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecoveryManifestWaitsForBothHistoryAndMutation(t *testing.T) {
	s, ctx := openLedger(t)
	seedRecoveryManifest(t, s, "shared", 1)
	_, err := s.sqlDB.ExecContext(ctx, `INSERT INTO source_history_entries(id,project_id,kind,state,undo_label,redo_label,undo_plan_json,redo_plan_json,created_at,updated_at) VALUES('history','p1','trash','applied','','','{"recovery_id":"shared"}','{}','','')`)
	testutil.FailErr(t, "claim history", err)
	_, err = s.sqlDB.ExecContext(ctx, `INSERT INTO source_mutations(id,project_id,kind,input_digest,plan_json,status,created_at,updated_at) VALUES('retry','p1','restore','digest','{"recovery_id":"shared"}','failed','','')`)
	testutil.FailErr(t, "claim retry", err)
	testutil.FailErr(t, "dequeue retained manifest", s.Retention.SweepBlobs(ctx))
	_, err = s.sqlDB.ExecContext(ctx, `UPDATE source_history_entries SET undo_plan_json='{}' WHERE id='history'`)
	testutil.FailErr(t, "release history reference", err)
	testutil.FailErr(t, "collect with retry retained", s.Retention.SweepBlobs(ctx))
	var count int
	testutil.FailErr(t, "count shared manifest", s.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_recovery_entries`).Scan(&count))
	if count != 1 {
		t.Fatal("history release discarded retry recovery")
	}
	_, err = s.sqlDB.ExecContext(ctx, `DELETE FROM source_mutations WHERE id='retry'`)
	testutil.FailErr(t, "retire retry", err)
	testutil.FailErr(t, "collect final claim", s.Retention.SweepBlobs(ctx))
	testutil.FailErr(t, "count released manifest", s.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_recovery_entries`).Scan(&count))
	if count != 0 {
		t.Fatal("final release did not reclaim manifest")
	}
}

func TestRecoveryQueueCascadesWithProject(t *testing.T) {
	s, ctx := openLedger(t)
	seedRecoveryManifest(t, s, "orphan", 1)
	_, err := s.sqlDB.ExecContext(ctx, `DELETE FROM projects WHERE id='p1'`)
	testutil.FailErr(t, "delete project", err)
	var count int
	testutil.FailErr(t, "count queue", s.sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM source_recovery_reclaim_queue`).Scan(&count))
	if count != 0 {
		t.Fatal("project left reclamation candidates")
	}
}

func TestRecoveryReclamationRollsBackTheWholePage(t *testing.T) {
	s, ctx := openLedger(t)
	seedRecoveryManifest(t, s, "orphan", 2)
	mustRecord(t, s, ctx, RecordInput{RecordLocation: RecordLocation{RootID: "r1", Path: "body.txt"}, ProjectID: "p1", Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("retained body")})
	_, err := s.sqlDB.ExecContext(ctx, `INSERT INTO source_recovery_objects(project_id,recovery_id,sha256) SELECT 'p1','orphan',sha256 FROM source_blob_objects LIMIT 1`)
	testutil.FailErr(t, "reference recovery object", err)
	_, err = s.sqlDB.ExecContext(ctx, `CREATE TRIGGER interrupt_recovery_reclaim BEFORE DELETE ON source_recovery_objects BEGIN SELECT RAISE(ABORT,'injected reclaim failure'); END`)
	testutil.FailErr(t, "interrupt object retirement", err)
	if _, err := s.Retention.reclaimRecoveryBatch(ctx); err == nil {
		t.Fatal("expected reclamation failure")
	}
	for table, want := range map[string]int{"source_recovery_entries": 2, "source_recovery_objects": 1, "source_recovery_reclaim_queue": 1} {
		var count int
		testutil.FailErr(t, "inspect rolled-back reclamation", s.sqlDB.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count))
		if count != want {
			t.Fatalf("%s has %d rows after rollback, want %d", table, count, want)
		}
	}
	_, err = s.sqlDB.ExecContext(ctx, `DROP TRIGGER interrupt_recovery_reclaim`)
	testutil.FailErr(t, "remove interruption", err)
	testutil.FailErr(t, "resume reclamation", s.Retention.SweepBlobs(ctx))
}
