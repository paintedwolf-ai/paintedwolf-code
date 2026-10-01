package worker

import (
	"errors"
	"github.com/lycaon/lycaon/internal/session"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"path/filepath"
	"testing"
	"time"
)

func TestPendingDecisionControlsDurableWorkerCompletion(t *testing.T) {
	for _, status := range []string{"partial", "complete", "needs_decision"} {
		t.Run(status, func(t *testing.T) {
			database := testdbfixture.Open(t, "store.db")
			testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
			testdbseed.InsertSession(t, database, "child", testdbseed.DefaultProjectID)
			queue := NewSQLQueue(database, 1)
			jobID, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
				ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
				AgentType: "implementer", Prompt: "Implement the backend", Brief: "Implement the backend",
				ExecutionTarget: api.ExecutionTargetLocal,
			})
			testutil.FailErr(t, "enqueue worker", err)
			claimed, err := queue.ClaimNext(t.Context(), ClaimRequest{ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
			testutil.FailErr(t, "claim worker", err)
			branch := filepath.Join(testbaseline.DataDir(t, database), "worker-branches", jobID)
			_, err = database.ExecContext(t.Context(), `UPDATE worker_jobs SET child_session_id = ?, workspace_relpath = ? WHERE id = ?`, "child", "worker-branches/"+jobID, jobID)
			testutil.FailErr(t, "bind child", err)
			decisions := session.NewSQLDecisionStore(database)
			testutil.FailErr(t, "request decision", decisions.Put(t.Context(), api.WorkerDecisionRequest{
				ChildSessionID: "child", WorkerID: jobID, Question: "Choose the contract", Options: []string{"A", "B"},
			}))
			won, err := queue.Complete(t.Context(), claimed, api.WorkerResult{Status: status, Summary: "Waiting for a decision"})
			testutil.FailErr(t, "settle worker", err)
			job, ok := NewSQLQueue(database, 1).Get(jobID)
			if !won || !ok || job.Status != api.WorkerStatusHeld || job.Result == nil || job.Result.Status != "needs_decision" || job.MergeStatus != "" || job.WorkspaceRoot != branch {
				t.Fatalf("decision lifecycle after reopen: won=%v job=%+v", won, job)
			}
			var attemptStatus string
			testutil.FailErr(t, "read attempt", database.QueryRowContext(t.Context(), `SELECT status FROM worker_attempts WHERE worker_job_id = ?`, jobID).Scan(&attemptStatus))
			if attemptStatus != "suspended" {
				t.Fatalf("attempt = %s, want suspended", attemptStatus)
			}
			decision, _, err := decisions.Get(t.Context(), "child")
			testutil.FailErr(t, "reload decision", err)
			resolver := NewSQLDecisionResolver(sessionstore.NewSQL(database), queue)
			testutil.FailErr(t, "answer decision", resolver.Resolve(t.Context(), decision, api.Message{Role: api.MessageRoleUser, Content: "A"}))
			job, _ = queue.Get(jobID)
			if job.Status != api.WorkerStatusPending || job.Result != nil || job.WorkspaceRoot != branch {
				t.Fatalf("answered worker did not resume: %+v", job)
			}
			_, pending, err := decisions.Get(t.Context(), "child")
			testutil.FailErr(t, "read resolved decision", err)
			if pending {
				t.Fatal("answered decision remains pending")
			}
		})
	}
}

func TestDecisionResolutionDistinguishesJobStateFromChangedQuestion(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "child", testdbseed.DefaultProjectID)
	_, err := database.ExecContext(t.Context(), `INSERT INTO worker_jobs(id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at) VALUES ('job', ?, 'parent', 'child', 'implementer', 'complete', 'work', 'work', ?)`, testdbseed.DefaultProjectID, time.Now().UTC().Format(time.RFC3339Nano))
	testutil.FailErr(t, "insert terminal job", err)
	decisions := session.NewSQLDecisionStore(database)
	testutil.FailErr(t, "store decision", decisions.Put(t.Context(), api.WorkerDecisionRequest{ChildSessionID: "child", WorkerID: "job", Question: "Choose", Options: []string{"A", "B"}}))
	decision, _, err := decisions.Get(t.Context(), "child")
	testutil.FailErr(t, "read decision", err)
	resolver := NewSQLDecisionResolver(sessionstore.NewSQL(database), NewSQLQueue(database, 1))
	err = resolver.Resolve(t.Context(), decision, api.Message{Role: api.MessageRoleUser, Content: "A"})
	var stateErr *DecisionStateError
	if !errors.As(err, &stateErr) || stateErr.Status != "complete" || errors.Is(err, errDecisionChanged) {
		t.Fatalf("unchanged decision returned %v", err)
	}
	decision.Question = "A different question"
	if err := resolver.Resolve(t.Context(), decision, api.Message{Role: api.MessageRoleUser, Content: "A"}); !errors.Is(err, errDecisionChanged) {
		t.Fatalf("changed question returned %v", err)
	}
}
