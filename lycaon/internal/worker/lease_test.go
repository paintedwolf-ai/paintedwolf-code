package worker

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestExpiredWorkerClaimRetriesAvailableAttempt(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	queue := NewSQLQueue(sqlDB, 1)
	queue.SetEventOutbox(eventoutbox.New(sqlDB, nil))
	testutil.FailErr(t, "insert", queue.store.InsertTask(t.Context(), api.WorkerTask{
		ID: "job-1", ProjectID: testdbseed.DefaultProjectID, AgentType: "implement",
		Prompt: "fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal, Status: api.WorkerStatusPending,
	}))
	claimed, err := queue.ClaimNext(t.Context(), ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "poller"})
	testutil.FailErr(t, "claim", err)
	if claimed.ClaimToken == "" || claimed.Attempt != 1 {
		t.Fatalf("claim = %+v", claimed)
	}
	testdbseed.InsertSession(t, sqlDB, "child-1", testdbseed.DefaultProjectID)
	testutil.FailErr(t, "bind child session", queue.SetChildSessionID(t.Context(), claimed.ID, "child-1"))
	branchRoot := filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches", "worker-branch")
	bound, err := queue.store.SetWorkerWorkspace(t.Context(), claimed.ID, branchRoot, testbaseline.Durable(t, sqlDB, claimed.ID, t.TempDir()))
	testutil.FailErr(t, "bind worker workspace", err)
	if !bound {
		t.Fatal("worker workspace was not bound")
	}
	ok, err := queue.RenewClaim(t.Context(), claimed.ID, "wrong-token")
	testutil.FailErr(t, "renew wrong token", err)
	if ok {
		t.Fatal("wrong token renewed claim")
	}
	_, err = sqlDB.ExecContext(t.Context(), `UPDATE worker_jobs SET lease_expires_at = ? WHERE id = ?`, db.FormatTime(time.Now().Add(-time.Minute)), claimed.ID)
	testutil.FailErr(t, "expire", err)
	recovered, err := queue.RecoverExpiredClaims(t.Context())
	testutil.FailErr(t, "recover", err)
	if len(recovered) != 0 {
		t.Fatalf("recovered = %+v", recovered)
	}
	retried, ok := queue.Get(claimed.ID)
	if !ok || retried.Status != api.WorkerStatusPending || retried.ClaimToken != "" ||
		retried.ChildSessionID != "child-1" || retried.WorkspaceRoot != branchRoot {
		t.Fatalf("retried = %+v", retried)
	}
	var events int
	testutil.FailErr(t, "count outbox events", sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM event_outbox WHERE topic = 'worker' AND project_id = ?`, testdbseed.DefaultProjectID).Scan(&events))
	if events < 3 {
		t.Fatalf("worker outbox events = %d want at least 3", events)
	}
}

func TestExpiredWorkerClaimFailsAfterAttemptBudget(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	queue := NewSQLQueue(sqlDB, 1)
	queue.SetEventOutbox(eventoutbox.New(sqlDB, nil))
	testutil.FailErr(t, "insert", queue.store.InsertTask(t.Context(), api.WorkerTask{
		ID: "job-exhausted", ProjectID: testdbseed.DefaultProjectID, AgentType: "implement",
		Prompt: "fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal, Status: api.WorkerStatusPending,
	}))
	for attempt := 1; attempt <= maxWorkerExecutionAttempts; attempt++ {
		claimed, claimErr := queue.ClaimNext(t.Context(), ClaimRequest{
			ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "poller",
		})
		testutil.FailErr(t, "claim", claimErr)
		if claimed.Attempt != attempt {
			t.Fatalf("attempt = %d want %d", claimed.Attempt, attempt)
		}
		_, err := sqlDB.ExecContext(t.Context(), `UPDATE worker_jobs SET lease_expires_at = ? WHERE id = ?`, db.FormatTime(time.Now().Add(-time.Minute)), claimed.ID)
		testutil.FailErr(t, "expire", err)
		recovered, recoverErr := queue.RecoverExpiredClaims(t.Context())
		testutil.FailErr(t, "recover", recoverErr)
		if attempt < maxWorkerExecutionAttempts && len(recovered) != 0 {
			t.Fatalf("attempt %d recovered terminally = %+v", attempt, recovered)
		}
		if attempt == maxWorkerExecutionAttempts {
			if len(recovered) != 1 || recovered[0].Status != api.WorkerStatusFailed {
				t.Fatalf("exhausted recovery = %+v", recovered)
			}
		}
	}
	var attempts, results int
	testutil.FailErr(t, "count attempts", sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM worker_attempts WHERE worker_job_id = 'job-exhausted'`).Scan(&attempts))
	testutil.FailErr(t, "count results", sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM worker_results WHERE worker_job_id = 'job-exhausted'`).Scan(&results))
	if attempts != maxWorkerExecutionAttempts || results != 1 {
		t.Fatalf("attempts/results = %d/%d", attempts, results)
	}
}

func TestLivePollerRescuesItsClaimBeforeExpirySweep(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	queue := NewSQLQueue(sqlDB, 1)
	queue.SetEventOutbox(eventoutbox.New(sqlDB, nil))
	testutil.FailErr(t, "insert", queue.store.InsertTask(t.Context(), api.WorkerTask{
		ID: "job-resume", ProjectID: testdbseed.DefaultProjectID, AgentType: "implement",
		Prompt: "fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal, Status: api.WorkerStatusPending,
	}))
	claimed, err := queue.ClaimNext(t.Context(), ClaimRequest{ClaimedBy: "poller"})
	testutil.FailErr(t, "claim", err)
	_, err = sqlDB.ExecContext(t.Context(), `UPDATE worker_jobs SET lease_expires_at = ? WHERE id = ?`, db.FormatTime(time.Now().Add(-time.Minute)), claimed.ID)
	testutil.FailErr(t, "expire", err)

	poller := NewLocalWorkerPoller(queue, nil, WorkersConfig{}, discardOutcomeRecorder{})
	_, execution := poller.registerExecution(t.Context(), claimed)
	defer poller.unregisterExecution(claimed.ID, execution)
	poller.renewActiveClaims(t.Context())
	recovered, err := queue.RecoverExpiredClaims(t.Context())
	testutil.FailErr(t, "recover after rescue", err)
	if len(recovered) != 0 {
		t.Fatalf("recovered live claim = %+v", recovered)
	}
}

func TestInMemoryExpiredClaimRetryWakesClaimer(t *testing.T) {
	queue := NewInMemoryQueue(1)
	jobID, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, Prompt: "fixture", Brief: "fixture",
		AgentType: "implement", ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue", err)
	select {
	case <-queue.RunnableWake():
	default:
	}
	claimed, err := queue.ClaimNext(t.Context(), ClaimRequest{ClaimedBy: "poller"})
	testutil.FailErr(t, "claim", err)
	past := time.Now().Add(-time.Minute)
	queue.mu.Lock()
	queue.jobs[jobID].task.LeaseExpiresAt = &past
	queue.mu.Unlock()

	recovered, err := queue.RecoverExpiredClaims(t.Context())
	testutil.FailErr(t, "recover", err)
	if len(recovered) != 0 {
		t.Fatalf("recovered = %+v", recovered)
	}
	retried, ok := queue.Get(claimed.ID)
	if !ok || retried.Status != api.WorkerStatusPending || retried.Error != workerClaimExpiredError {
		t.Fatalf("retried = %+v", retried)
	}
	select {
	case <-queue.RunnableWake():
	default:
		t.Fatal("retry did not wake the claimer")
	}
}
