package instructions

import (
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
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
	testutil.FailErr(t, "seed terminal plan", plans.Set("session", "- [x] Finished"))
	svc := New(sessions, nil, nil, nil)
	svc.SetProgress(plans)
	svc.SetWorkflow(&WorkflowDomains{Runs: stubWorkflowRuns{run: &api.WorkflowRun{ID: "run"}}})
	message := api.Message{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Next request"}
	_, err := database.ExecContext(t.Context(), `CREATE TRIGGER refuse_bootstrap BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT,'unavailable'); END`)
	testutil.FailErr(t, "refuse bootstrap row", err)
	if err := svc.BootstrapProgress(t.Context(), "session", message); err == nil {
		t.Fatal("bootstrap swallowed persistence failure")
	}
	if plans.Get(t.Context(), "session") != "- [x] Finished" {
		t.Fatal("failed bootstrap changed plan")
	}
	_, err = database.ExecContext(t.Context(), `DROP TRIGGER refuse_bootstrap`)
	testutil.FailErr(t, "allow bootstrap row", err)
	testutil.FailErr(t, "retry bootstrap", svc.BootstrapProgress(t.Context(), "session", message))
	rows, err := sessions.GetMessages(t.Context(), "session")
	testutil.FailErr(t, "read bootstrap row", err)
	if len(rows) != 2 || rows[1].ProgressUpdate == nil || rows[1].WorkflowRunID != "run" {
		t.Fatalf("bootstrap rows=%+v", rows)
	}
}
