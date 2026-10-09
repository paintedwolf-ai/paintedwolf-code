package review

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReviewerScopePreservesExplicitRunsAndIsolatesAmbientIntents(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "reviewer-scope.db")
	testdbseed.InsertSessionWithRoot(t, sqlDB, "sess-1", testdbseed.DefaultProjectID, t.TempDir())
	verdicts := &Verdicts{Sessions: store.NewSQL(sqlDB)}
	now := time.Now().UTC()
	testutil.FailErr(t, "append current request", verdicts.Sessions.AppendMessages(t.Context(), "sess-1", api.Message{ID: "request", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "New implementation", CreatedAt: now}))
	reviewer := api.WorkerTask{ID: "review", WorkflowRunID: "run", WorkflowPhase: "judge", AgentType: "skeptic", Status: api.WorkerStatusComplete, CreatedAt: now.Add(-time.Second), Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}
	workflowTaskQuery1 := func(context.Context, string) ([]api.WorkerTask, error) { return []api.WorkerTask{reviewer}, nil }
	verdicts.WorkerTasks = workflowTaskQuery1
	run := &api.WorkflowRun{ID: "run", SessionID: "sess-1", CurrentPhase: "judge"}
	if missing := verdicts.missingReviewAgents(t.Context(), run, []string{"skeptic"}); len(missing) != 0 {
		t.Fatal("explicit workflow lost a completed review after new user direction")
	}
	run.AttachPolicy = "session_create"
	if missing := verdicts.missingReviewAgents(t.Context(), run, []string{"skeptic"}); len(missing) != 1 {
		t.Fatal("ambient workflow reused an earlier intent's review")
	}
	reviewer.CreatedAt = now.Add(time.Second)
	if missing := verdicts.missingReviewAgents(t.Context(), run, []string{"skeptic"}); len(missing) != 0 {
		t.Fatal("ambient workflow lost the current intent's review")
	}
	run.AttachPolicy = ""
	reviewer.WorkflowRunID = "other-run"
	if missing := verdicts.missingReviewAgents(t.Context(), run, []string{"skeptic"}); len(missing) != 1 {
		t.Fatal("another workflow supplied the required review")
	}
}
