package session

import (
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

func TestSQLDecisionStoreSurvivesReconstruction(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "child-1", testdbseed.DefaultProjectID)
	_, err := database.ExecContext(t.Context(), `
		INSERT INTO worker_jobs(id, project_id, child_session_id, agent_type, status, prompt, brief, created_at)
		VALUES (?, ?, ?, 'implementer', 'held', 'fixture', 'fixture', ?)`,
		"job-1", testdbseed.DefaultProjectID, "child-1", time.Now().UTC().Format(time.RFC3339Nano))
	testutil.FailErr(t, "insert worker job", err)

	store := NewSQLDecisionStore(database)
	testutil.FailErr(t, "put decision", store.Put(t.Context(), api.WorkerDecisionRequest{
		ChildSessionID: "child-1", WorkerID: "job-1", Question: "Choose one",
		Options: []string{"A", "B"}, BlockerClass: api.WorkerBlockerDecision,
	}))

	reopened := NewSQLDecisionStore(database)
	decision, ok, err := reopened.Get(t.Context(), "child-1")
	testutil.FailErr(t, "get decision", err)
	if !ok || decision.WorkerID != "job-1" || len(decision.Options) != 2 {
		t.Fatalf("decision = %+v ok=%v", decision, ok)
	}
}
