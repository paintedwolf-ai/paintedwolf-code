package progress_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRunRefreshUsesTransactionalPredecessor(t *testing.T) {
	for _, kind := range []string{"bootstrap", "adoption"} {
		t.Run(kind, func(t *testing.T) {
			f := newProgressFixture(t)
			replacePlan(t, f.plans, progress.RowSpec{Label: "Deliver", State: progress.ProgressStatePending})
			done := progress.ProgressStateDone
			before := updatePlan(t, f.plans, progress.UpdateRowSpec{ID: "p1", State: &done})
			target := "run"
			refresh := progress.EnsureBootstrap
			if kind == "adoption" {
				target = "child"
				refresh = progress.AdoptActiveRun
				_, err := f.database.ExecContext(t.Context(), `INSERT INTO workflow_runs (id,session_id,project_id,workflow_id,workflow_version,status,current_phase,created_at,updated_at,parent_run_id) SELECT 'child',session_id,project_id,workflow_id,workflow_version,status,current_phase,created_at,updated_at,id FROM workflow_runs WHERE id='run'`)
				testutil.FailErr(t, "insert next run", err)
			}
			_, err := f.database.ExecContext(t.Context(), `CREATE TRIGGER reject_reset BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT,'reset refused'); END`)
			testutil.FailErr(t, "refuse reset publication", err)
			changed, err := refresh(t.Context(), f.plans, "session", target)
			if err == nil || changed {
				t.Fatalf("failed refresh: changed=%v error=%v", changed, err)
			}
			after, err := f.plans.Snapshot(t.Context(), "session")
			testutil.FailErr(t, "read failed reset", err)
			if !before.Equal(after) || len(f.messages(t)) != 2 {
				t.Fatal("failed refresh partially committed")
			}
			_, err = f.database.ExecContext(t.Context(), `DROP TRIGGER reject_reset`)
			testutil.FailErr(t, "allow reset publication", err)
			changed, err = refresh(t.Context(), f.plans, "session", target)
			testutil.FailErr(t, "refresh plan", err)
			if !changed {
				t.Fatal("refresh did not commit")
			}
			rows := f.messages(t)
			if len(rows) != 3 || rows[2].WorkflowRunID != target || len(rows[2].ProgressUpdate.Changes) != 1 {
				t.Fatalf("reset row=%+v", rows)
			}
			changed, err = refresh(t.Context(), f.plans, "session", target)
			testutil.FailErr(t, "repeat refresh", err)
			if changed || len(f.messages(t)) != 3 {
				t.Fatal("repeat refresh duplicated transition")
			}
		})
	}
}

func TestUnboundPlanAdoptionPreservesRowsAndItemIdentity(t *testing.T) {
	f := newProgressFixture(t)
	_, err := f.database.ExecContext(t.Context(), `
		INSERT INTO session_progress(session_id,workflow_run_id,updated_at,document_version,next_item_seq) VALUES('session','','2026-01-01T00:00:00Z',1,2);
		INSERT INTO session_progress_items(session_id,item_id,ord,state,label) VALUES('session','p1',0,'pending','Seeded');
	`)
	testutil.FailErr(t, "seed unbound plan", err)
	changed, err := progress.AdoptActiveRun(t.Context(), f.plans, "session", "run")
	testutil.FailErr(t, "adopt seeded plan", err)
	if changed {
		t.Fatal("adoption reported a reset")
	}
	snapshot, err := f.plans.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read adopted plan", err)
	if snapshot.RunID != "run" || len(snapshot.Rows) != 1 || snapshot.Rows[0].ID != "p1" || snapshot.Rows[0].Label != "Seeded" || len(f.messages(t)) != 0 {
		t.Fatalf("adoption changed visible plan: %+v", snapshot)
	}
	done := progress.ProgressStateDone
	updatePlan(t, f.plans, progress.UpdateRowSpec{ID: "p1", State: &done})
	rows := f.messages(t)
	if len(rows) != 1 || rows[0].ProgressComplete == nil || rows[0].WorkflowRunID != "run" {
		t.Fatalf("adopted completion=%+v", rows)
	}
}

func TestVisiblePlanWithoutWorkflowRunIsRefused(t *testing.T) {
	f := newProgressFixture(t)
	_, err := f.database.ExecContext(t.Context(), `DELETE FROM workflow_runs WHERE id='run'`)
	testutil.FailErr(t, "remove active run", err)
	if _, err := f.plans.Apply(t.Context(), "session", progress.Mutation{
		Replace: []progress.RowSpec{{Label: "Unbound", State: progress.ProgressStatePending}},
	}, progress.ApplyOptions{}); err == nil {
		t.Fatal("unbound visible mutation accepted")
	}
	snap, err := f.plans.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read snapshot", err)
	if len(f.messages(t)) != 0 || !snap.Missing() {
		t.Fatal("unbound mutation partially committed")
	}
}
