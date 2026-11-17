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
			applyPlan(t, f.plans, "- [ ] Deliver")
			before := applyPlan(t, f.plans, "- [x] Deliver")
			target := "run"
			refresh := progress.EnsureBootstrap
			if kind == "adoption" {
				target = "child"
				refresh = progress.AdoptActiveRun
				_, err := f.database.ExecContext(t.Context(), `INSERT INTO workflow_runs (id,session_id,project_id,workflow_id,workflow_version,status,current_phase,created_at,updated_at) SELECT 'child',session_id,project_id,workflow_id,workflow_version,status,current_phase,created_at,updated_at FROM workflow_runs WHERE id='run'`)
				testutil.FailErr(t, "insert next run", err)
			}
			_, err := f.database.ExecContext(t.Context(), `CREATE TRIGGER reject_reset BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT,'reset refused'); END`)
			testutil.FailErr(t, "refuse reset publication", err)
			changed, err := refresh(t.Context(), f.plans, "session", target, "Next request")
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
			changed, err = refresh(t.Context(), f.plans, "session", target, "Next request")
			testutil.FailErr(t, "refresh plan", err)
			if !changed {
				t.Fatal("refresh did not commit")
			}
			rows := f.messages(t)
			if len(rows) != 3 || rows[2].WorkflowRunID != target || len(rows[2].ProgressUpdate.Changes) != 1 {
				t.Fatalf("reset row=%+v", rows)
			}
			changed, err = refresh(t.Context(), f.plans, "session", target, "Repeated request")
			testutil.FailErr(t, "repeat refresh", err)
			if changed || len(f.messages(t)) != 3 {
				t.Fatal("repeat refresh duplicated transition")
			}
		})
	}
}

func TestUnboundPlanAdoptionPreservesContentAndItemIdentity(t *testing.T) {
	f := newProgressFixture(t)
	_, err := f.database.ExecContext(t.Context(), `INSERT INTO session_progress(session_id,workflow_run_id,content,item_ids_json,document_version,updated_at) VALUES('session','','- [ ] Seeded','["seeded-id"]',1,'2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed unbound plan", err)
	changed, err := progress.AdoptActiveRun(t.Context(), f.plans, "session", "run", "New goal")
	testutil.FailErr(t, "adopt seeded plan", err)
	if changed {
		t.Fatal("adoption reported a reset")
	}
	snapshot, err := f.plans.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read adopted plan", err)
	if snapshot.RunID != "run" || snapshot.Content != "- [ ] Seeded" || len(snapshot.ItemIDs) != 1 || snapshot.ItemIDs[0] != "seeded-id" || len(f.messages(t)) != 0 {
		t.Fatalf("adoption changed visible plan: %+v", snapshot)
	}
	applyPlan(t, f.plans, "- [x] Seeded")
	rows := f.messages(t)
	if len(rows) != 1 || rows[0].ProgressComplete == nil || rows[0].WorkflowRunID != "run" {
		t.Fatalf("adopted completion=%+v", rows)
	}
}

func TestVisiblePlanWithoutWorkflowRunIsRefused(t *testing.T) {
	f := newProgressFixture(t)
	_, err := f.database.ExecContext(t.Context(), `DELETE FROM workflow_runs WHERE id='run'`)
	testutil.FailErr(t, "remove active run", err)
	if err := f.plans.Set("session", "- [ ] Unbound"); err == nil {
		t.Fatal("unbound visible mutation accepted")
	}
	if len(f.messages(t)) != 0 || f.plans.Get(t.Context(), "session") != "" {
		t.Fatal("unbound mutation partially committed")
	}
}
