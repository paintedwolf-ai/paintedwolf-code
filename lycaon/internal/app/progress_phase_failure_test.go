package app

import (
	"testing"

	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/app/workerruntime"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
)

func TestPhaseEntryStopsWhenProgressCannotCommit(t *testing.T) {
	database := testdbfixture.Open(t, "phase-progress-failure")
	testdbseed.InsertWorkflowRun(t, database, "parent", "session", testdbseed.DefaultProjectID)
	records := store.NewSQL(database)
	plans := progress.NewSQLStore(database, records)
	testutil.FailErr(t, "seed parent plan", plans.Set("session", "- [ ] Retain"))
	_, err := database.ExecContext(t.Context(), `INSERT INTO workflow_runs(id,session_id,project_id,workflow_id,workflow_version,status,current_phase,parent_run_id,created_at,updated_at) SELECT 'child',session_id,project_id,workflow_id,workflow_version,status,current_phase,id,created_at,updated_at FROM workflow_runs WHERE id='parent'`)
	testutil.FailErr(t, "insert child run", err)
	_, err = database.ExecContext(t.Context(), `CREATE TRIGGER refuse_phase_plan BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT,'unavailable'); END`)
	testutil.FailErr(t, "refuse phase plan row", err)
	host := session.NewHost(records, session.Models{Limits: settings.DefaultSessionLimits()}, tools.NewStubRegistry())
	runtime := workerruntime.New(database, nil, worker.WorkersConfig{})
	runtime.SetDependencies(workerruntime.Dependencies{Sessions: &sessions.Runtime{Manager: host}, Progress: func() progress.Store { return plans }})
	err = runtime.OnWorkflowPhaseEnter(t.Context(), &workflowphases.RunContext{SessionID: "session", RunID: "child", Phase: "work"}, workflowdef.PhaseDef{ID: "work"})
	if err == nil {
		t.Fatal("phase entry continued after failed plan persistence")
	}
	snapshot, err := plans.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read retained parent plan", err)
	rows, err := records.GetMessages(t.Context(), "session")
	testutil.FailErr(t, "read retained parent transcript", err)
	if snapshot.RunID != "parent" || snapshot.Content != "- [ ] Retain" || len(rows) != 1 {
		t.Fatalf("partial phase plan: %+v rows=%d", snapshot, len(rows))
	}
}
