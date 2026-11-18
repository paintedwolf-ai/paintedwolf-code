package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/session/workerresults"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/toolstest"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type allowAllWorkflowRuns struct{}

func (allowAllWorkflowRuns) AssertRunnable(context.Context, string) error { return nil }

func (allowAllWorkflowRuns) AssertWorkerTask(context.Context, *api.WorkerTask) error { return nil }

func TestRunStopServiceCancelProjectsWorkerCard(t *testing.T) {
	ctx := context.Background()
	store := storetest.New(t, testdbseed.DefaultProjectID)
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, toolstest.NewRegistry())
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	runID := uuid.NewString()
	jobID := uuid.NewString()
	queue := newTestQueue(t, 2, testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, queue.db, runID, sess.ID, testdbseed.DefaultProjectID)
	queue.SetWorkflowDomains(&WorkflowDomains{Runs: allowAllWorkflowRuns{}, Tasks: allowAllWorkflowRuns{}})
	_, err = queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ID:              jobID,
		ParentSessionID: sess.ID,
		ProjectID:       testdbseed.DefaultProjectID,
		WorkflowRunID:   runID,
		AgentType:       "implementer",
		Status:          api.WorkerStatusPending,
	})
	testutil.FailErr(t, "enqueue", err)
	requested, err := queue.RequestCancellation(ctx, jobID)
	testutil.FailErr(t, "request cancellation", err)
	if !requested {
		t.Fatal("cancellation request rejected")
	}

	canonicalID := uuid.NewString()
	enqueue := `{"job_id":"` + jobID + `","status":"enqueued"}`
	testutil.FailErr(t, "seed canonical row", store.AppendMessages(ctx, sess.ID, api.Message{
		ID:         canonicalID,
		Role:       api.MessageRoleTool,
		Content:    enqueue,
		ToolResult: &api.ToolResult{Tool: "task", Content: enqueue, Dispatch: &api.WorkerDispatch{WorkerID: jobID}},
	}))

	svc := &RunStopService{Queue: queue, Holds: mgr.Workers.Cards, Cancellations: mgr.Workers.Cancellations}
	testutil.FailErr(t, "cancel workers by run", svc.CancelWorkersByRunID(ctx, runID, "workflow stopped"))

	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	if len(msgs) != 1 {
		t.Fatalf("message count = %d want 1 canonical row", len(msgs))
	}
	row := msgs[0]
	if row.ID != canonicalID {
		t.Fatalf("row id = %q want canonical %q", row.ID, canonicalID)
	}
	if row.WorkerSummary == nil || row.WorkerSummary.Status != api.WorkerSummaryStatusCanceled {
		t.Fatalf("worker_summary status = %+v want canceled", row.WorkerSummary)
	}
	got, ok := queue.Get(jobID)
	if !ok || got.Status != api.WorkerStatusCanceled || got.Result == nil {
		t.Fatalf("queue status = %+v want canceled", got)
	}
}

// poisonRunStopSession records settlements and rejects one job.
type poisonRunStopSession struct {
	failJobID string
	canceled  []string
	held      []string
}

func (p *poisonRunStopSession) Append(_ context.Context, _ string, in workeroutcomes.CancellationInput) error {
	if in.JobID == p.failJobID {
		return errors.New("poison append")
	}
	p.canceled = append(p.canceled, in.JobID)
	return nil
}

func (p *poisonRunStopSession) Hold(_ context.Context, _ string, in workerresults.HoldInput) error {
	if in.JobID == p.failJobID {
		return errors.New("poison append")
	}
	p.held = append(p.held, in.JobID)
	return nil
}

func TestRunStopServiceCancelSettlesRemainingTasksPastPoisonTask(t *testing.T) {
	ctx := context.Background()
	runID := uuid.NewString()
	poisonID := uuid.NewString()
	victimID := uuid.NewString()
	queue := newTestQueue(t, 2, testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, queue.db, runID, "parent", testdbseed.DefaultProjectID)
	queue.SetWorkflowDomains(&WorkflowDomains{Runs: allowAllWorkflowRuns{}, Tasks: allowAllWorkflowRuns{}})
	for _, jobID := range []string{poisonID, victimID} {
		_, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
			Prompt:          "fixture",
			Brief:           "fixture",
			ID:              jobID,
			ParentSessionID: "parent",
			ProjectID:       testdbseed.DefaultProjectID,
			WorkflowRunID:   runID,
			AgentType:       "implementer",
			Status:          api.WorkerStatusPending,
		})
		testutil.FailErr(t, "enqueue", err)
	}

	sessions := &poisonRunStopSession{failJobID: poisonID}
	svc := &RunStopService{Queue: queue, Holds: sessions, Cancellations: sessions}
	err := svc.CancelWorkersByRunID(ctx, runID, "workflow stopped")
	if err == nil {
		t.Fatal("cancel workers by run: want joined poison error, got nil")
	}
	for _, jobID := range []string{poisonID, victimID} {
		got, ok := queue.Get(jobID)
		if !ok || got.Status != api.WorkerStatusCanceled {
			t.Fatalf("job %s queue status = %+v want canceled despite poison task", jobID, got)
		}
	}
	if len(sessions.canceled) != 1 || sessions.canceled[0] != victimID {
		t.Fatalf("settled jobs = %v want [%s]", sessions.canceled, victimID)
	}
}

func TestRunStopServiceHoldContinuesPastPoisonTask(t *testing.T) {
	ctx := context.Background()
	runID := uuid.NewString()
	poisonID := uuid.NewString()
	victimID := uuid.NewString()
	queue := newTestQueue(t, 2, testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, queue.db, runID, "parent", testdbseed.DefaultProjectID)
	queue.SetWorkflowDomains(&WorkflowDomains{Runs: allowAllWorkflowRuns{}, Tasks: allowAllWorkflowRuns{}})
	for _, jobID := range []string{poisonID, victimID} {
		_, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
			Prompt:          "fixture",
			Brief:           "fixture",
			ID:              jobID,
			ParentSessionID: "parent",
			ProjectID:       testdbseed.DefaultProjectID,
			WorkflowRunID:   runID,
			AgentType:       "implementer",
			Status:          api.WorkerStatusPending,
		})
		testutil.FailErr(t, "enqueue", err)
	}

	sessions := &poisonRunStopSession{failJobID: poisonID}
	svc := &RunStopService{Queue: queue, Holds: sessions, Cancellations: sessions}
	err := svc.HoldPendingWorkersByRunID(ctx, runID)
	if err == nil {
		t.Fatal("hold workers by run: want joined poison error, got nil")
	}
	for _, jobID := range []string{poisonID, victimID} {
		got, ok := queue.Get(jobID)
		if !ok || got.Status != api.WorkerStatusHeld {
			t.Fatalf("job %s queue status = %+v want held despite poison task", jobID, got)
		}
	}
	if len(sessions.held) != 1 || sessions.held[0] != victimID {
		t.Fatalf("held jobs = %v want [%s]", sessions.held, victimID)
	}
}

func TestRunStopServiceHoldPatchesCanonicalWorkerRow(t *testing.T) {
	ctx := context.Background()
	store := storetest.New(t, testdbseed.DefaultProjectID)
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, toolstest.NewRegistry())
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	runID := uuid.NewString()
	jobID := uuid.NewString()
	queue := newTestQueue(t, 2, testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, queue.db, runID, sess.ID, testdbseed.DefaultProjectID)
	queue.SetWorkflowDomains(&WorkflowDomains{Runs: allowAllWorkflowRuns{}, Tasks: allowAllWorkflowRuns{}})
	_, err = queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ID:              jobID,
		ParentSessionID: sess.ID,
		ProjectID:       testdbseed.DefaultProjectID,
		WorkflowRunID:   runID,
		AgentType:       "implementer",
		Status:          api.WorkerStatusPending,
	})
	testutil.FailErr(t, "enqueue", err)
	testutil.FailErr(t, "hold worker", queue.Hold(t.Context(), jobID))

	canonicalID := uuid.NewString()
	enqueue := `{"job_id":"` + jobID + `","status":"enqueued"}`
	testutil.FailErr(t, "seed canonical row", store.AppendMessages(ctx, sess.ID, api.Message{
		ID:         canonicalID,
		Role:       api.MessageRoleTool,
		Content:    enqueue,
		ToolResult: &api.ToolResult{Tool: "task", Content: enqueue, Dispatch: &api.WorkerDispatch{WorkerID: jobID}},
	}))

	svc := &RunStopService{Queue: queue, Holds: mgr.Workers.Cards, Cancellations: mgr.Workers.Cancellations}
	testutil.FailErr(t, "hold workers by run", svc.HoldPendingWorkersByRunID(ctx, runID))

	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	if len(msgs) != 1 {
		t.Fatalf("message count = %d want 1 canonical row", len(msgs))
	}
	if msgs[0].WorkerSummary == nil || msgs[0].WorkerSummary.Status != api.WorkerSummaryStatus("held") {
		t.Fatalf("worker_summary status = %+v want held", msgs[0].WorkerSummary)
	}
	got, ok := queue.Get(jobID)
	if !ok || got.Status != api.WorkerStatusHeld {
		t.Fatalf("queue status = %+v want held", got)
	}
}

func TestRunStopServiceWithdrawsUnstartedRunWorkersAndKeepsStarted(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "run_stop.db")
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-stop-1", "coord-1", testdbseed.DefaultProjectID)
	records := workerrecords.New(sqlDB)
	actor := workerrecords.Actor{SessionID: "coord-1", Role: workerrecords.RoleCoordinator, PrincipalID: "coord-1"}

	unstarted, err := records.CreateWorker(t.Context(), actor, workerrecords.Op{ID: "op-unstarted"}, workerrecords.WorkerSpec{
		Name: "planned-idle", AgentType: "implementer", ScopeMode: workerrecords.ScopeWrite, Brief: "todo", WorkflowRunID: "run-stop-1", WorkflowWorkID: "leg-idle",
	})
	testutil.FailErr(t, "create unstarted worker", err)

	started, err := records.CreateWorker(t.Context(), actor, workerrecords.Op{ID: "op-started"}, workerrecords.WorkerSpec{
		Name: "planned-busy", AgentType: "implementer", ScopeMode: workerrecords.ScopeWrite, Brief: "running", WorkflowRunID: "run-stop-1", WorkflowWorkID: "leg-busy",
	})
	testutil.FailErr(t, "create started worker", err)
	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO worker_jobs (id, project_id, parent_session_id, agent_type, status, prompt, brief, created_at)
		VALUES ('job-busy-1', ?, 'coord-1', 'implementer', 'running', 'p', 'b', '2026-01-01T00:00:00Z')
	`, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "insert started job", err)
	_, err = records.AdmitRound(t.Context(), actor, workerrecords.Op{ID: "op-round"}, started.WorkerID, workerrecords.RoundSpec{
		JobID: "job-busy-1", ExecutionTarget: "local",
	})
	testutil.FailErr(t, "admit round", err)

	svc := &RunStopService{Records: records}
	testutil.FailErr(t, "empty run id", svc.WithdrawPlannedWorkersByRunID(t.Context(), " "))
	testutil.FailErr(t, "withdraw planned workers by run", svc.WithdrawPlannedWorkersByRunID(t.Context(), "run-stop-1"))
	wIdle, err := records.Worker(t.Context(), actor, unstarted.WorkerID)
	testutil.FailErr(t, "load idle worker", err)
	if wIdle.ClosedReason != "withdrawn" || wIdle.ClosedAt == "" {
		t.Fatalf("unstarted worker not withdrawn: %+v", wIdle)
	}
	wBusy, err := records.Worker(t.Context(), actor, started.WorkerID)
	testutil.FailErr(t, "load busy worker", err)
	if wBusy.ClosedAt != "" {
		t.Fatalf("started worker closed during withdraw: %+v", wBusy)
	}
}
