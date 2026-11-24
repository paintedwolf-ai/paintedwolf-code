package instructions

import (
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	api "github.com/lycaon/lycaon/pkg/api"
)

func TestInstructionBootstrapReportsPersistenceFailure(t *testing.T) {
	database := testdbfixture.Open(t, "instruction-progress")
	testdbseed.InsertWorkflowRun(t, database, "run", "session", testdbseed.DefaultProjectID)
	sessions := sessionstore.NewSQL(database)
	plans := progress.NewSQLStore(database, sessions)
	seed := progresstest.SeedRows(t, plans, "session", progress.RowSpec{Label: "Deliver"})
	progresstest.UpdateRows(t, plans, "session", progress.UpdateRowSpec{ID: seed.Rows[0].ID, State: progresstest.State("done")})

	svc := New(sessions.Sessions, sessions.Transcript, nil, nil, nil)
	svc.SetProgress(plans)
	svc.SetWorkflow(&WorkflowDomains{Runs: stubWorkflowRuns{run: &api.WorkflowRun{ID: "run"}}})
	message := api.Message{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Next request"}
	_, err := database.ExecContext(t.Context(), `CREATE TRIGGER refuse_bootstrap BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT,'unavailable'); END`)
	testutil.FailErr(t, "refuse bootstrap row", err)
	if err := svc.BootstrapProgress(t.Context(), "session", message); err == nil {
		t.Fatal("bootstrap swallowed persistence failure")
	}
	stillThere, err := plans.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "snapshot after failed bootstrap", err)
	if len(stillThere.Rows) != 1 || stillThere.Rows[0].State != progress.ProgressStateDone {
		t.Fatalf("failed bootstrap changed plan: %+v", stillThere.Rows)
	}
	_, err = database.ExecContext(t.Context(), `DROP TRIGGER refuse_bootstrap`)
	testutil.FailErr(t, "allow bootstrap row", err)
	testutil.FailErr(t, "retry bootstrap", svc.BootstrapProgress(t.Context(), "session", message))
	rows, err := sessions.GetMessages(t.Context(), "session")
	testutil.FailErr(t, "read bootstrap row", err)
	if len(rows) != 3 || rows[2].ProgressUpdate == nil || rows[2].WorkflowRunID != "run" {
		t.Fatalf("bootstrap rows=%+v", rows)
	}
}
