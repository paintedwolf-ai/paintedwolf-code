package workertopology_test

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/workertopologytest"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workertopology"
	"github.com/lycaon/lycaon/pkg/api"
)

type allowWorkflow struct{}

func (allowWorkflow) AssertRunnable(context.Context, string) error            { return nil }
func (allowWorkflow) AssertWorkerTask(context.Context, *api.WorkerTask) error { return nil }

func insertWorkflowRun(t *testing.T, sqlDB *db.Store, runID, sessionID string) {
	t.Helper()
	now := timelayout.Format(time.Now().UTC())
	_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO workflow_runs (
			id, project_id, session_id, workflow_id, workflow_version, status, current_phase,
			revision, created_at, updated_at
		) VALUES (?, ?, ?, 'bugbash', '1.0.0', 'running', 'triage', 1, ?, ?)`,
		runID, testdbseed.DefaultProjectID, sessionID, now, now)
	testutil.FailErr(t, "insert workflow_run "+runID, err)
}

func TestRuntimeBatchDispatchReloadAndAbort(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "topology.db")
	const sessionID = "coord-topo"
	const runID = "wf-run-1"
	testdbseed.InsertSession(t, sqlDB, sessionID, testdbseed.DefaultProjectID)
	insertWorkflowRun(t, sqlDB, runID, sessionID)

	records := workerrecords.New(sqlDB)
	queue := worker.NewSQLServices(sqlDB, 8).Queue
	queue.SetWorkflowDomains(&worker.WorkflowDomains{Runs: allowWorkflow{}, Tasks: allowWorkflow{}})
	rt := workertopology.New(records, queue)
	ctx := t.Context()

	batch, units, err := rt.CreateBatch(ctx, orchestration.TopologyBatch{
		SessionID:     sessionID,
		ProjectID:     testdbseed.DefaultProjectID,
		WorkspacePath: t.TempDir(),
		WorkflowRunID: runID,
		Pattern:       orchestration.TopologyPipeline,
	}, []orchestration.TopologyUnit{
		{ID: "u-read", WorkID: "stage-scan", AgentType: "path-explorer", Prompt: "Scan paths", Phase: "triage"},
		{ID: "u-write", ParentID: "u-read", WorkID: "stage-fix", AgentType: "implementer", Prompt: "Fix bug", Phase: "triage"},
		{ID: "u-idle", WorkID: "stage-verify", AgentType: "code-reviewer", Prompt: "Review fix", Phase: "triage"},
	})
	testutil.FailErr(t, "CreateBatch", err)
	if len(units) != 3 || units[1].ParentID != units[0].ID {
		t.Fatalf("created units = %+v", units)
	}

	gotBySession, ok, err := rt.BatchBySession(ctx, sessionID)
	testutil.FailErr(t, "BatchBySession", err)
	if !ok || gotBySession != batch.ID {
		t.Fatalf("BatchBySession = (%q, %v), want (%q, true)", gotBySession, ok, batch.ID)
	}

	updated := units[0]
	updated.Prompt = "Scan paths thoroughly"
	testutil.FailErr(t, "UpdateUnit", rt.UpdateUnit(ctx, batch.ID, updated))

	settledRead, err := workertopologytest.DispatchAndSettle(ctx, rt, queue, batch.ID, units[0].ID, api.WorkerResult{
		Status:  "complete",
		Summary: "Scanned target tree.",
	})
	testutil.FailErr(t, "DispatchAndSettle complete", err)
	if settledRead.Status != api.LegStatusComplete || settledRead.Summary != "Scanned target tree." {
		t.Fatalf("settledRead = %+v", settledRead)
	}

	dispatchedWrite, err := rt.DispatchUnit(ctx, batch.ID, units[1].ID)
	testutil.FailErr(t, "DispatchUnit", err)
	if dispatchedWrite.Status != api.LegStatusDispatched || dispatchedWrite.JobID == "" {
		t.Fatalf("dispatchedWrite = %+v", dispatchedWrite)
	}

	statuses, err := rt.WorkflowWorkStatuses(ctx, runID)
	testutil.FailErr(t, "WorkflowWorkStatuses", err)
	if statuses["stage-scan"] != api.LegStatusComplete || statuses["stage-fix"] != api.LegStatusDispatched || statuses["stage-verify"] != api.LegStatusPending {
		t.Fatalf("WorkflowWorkStatuses = %+v", statuses)
	}

	cold := workertopology.New(records, queue)
	reloadedID, ok, err := cold.BatchByWorkflowRun(ctx, runID)
	testutil.FailErr(t, "cold BatchByWorkflowRun", err)
	if !ok || reloadedID == "" {
		t.Fatalf("cold BatchByWorkflowRun = (%q, %v)", reloadedID, ok)
	}
	reloadedBatch, err := cold.GetBatch(ctx, reloadedID)
	testutil.FailErr(t, "cold GetBatch", err)
	if reloadedBatch.Status != orchestration.TopologyBatchActive {
		t.Fatalf("cold batch status = %q, want active", reloadedBatch.Status)
	}

	testutil.FailErr(t, "AbortBatch", rt.AbortBatch(ctx, batch.ID, "user canceled"))
	aborted, err := rt.GetBatch(ctx, batch.ID)
	testutil.FailErr(t, "GetBatch after abort", err)
	if aborted.Status != orchestration.TopologyBatchAborted {
		t.Fatalf("aborted status = %q, want aborted", aborted.Status)
	}
}

func TestRuntimeDispatchAndSettleFailedStage(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "topology-fail.db")
	const sessionID = "coord-topo-fail"
	testdbseed.InsertSession(t, sqlDB, sessionID, testdbseed.DefaultProjectID)

	records := workerrecords.New(sqlDB)
	queue := worker.NewSQLServices(sqlDB, 4).Queue
	rt := workertopology.New(records, queue)
	ctx := t.Context()

	batch, units, err := rt.CreateBatch(ctx, orchestration.TopologyBatch{
		SessionID: sessionID,
		ProjectID: testdbseed.DefaultProjectID,
	}, []orchestration.TopologyUnit{
		{ID: "u1", WorkID: "stage-1", AgentType: "path-explorer", Prompt: "Probe"},
	})
	testutil.FailErr(t, "CreateBatch", err)

	failedUnit, err := workertopologytest.DispatchAndSettle(ctx, rt, queue, batch.ID, units[0].ID, api.WorkerResult{
		Status:  "failed",
		Summary: "Probe failed",
	})
	testutil.FailErr(t, "DispatchAndSettle failed", err)
	if failedUnit.Status != api.LegStatusFailed {
		t.Fatalf("failedUnit = %+v", failedUnit)
	}
	gotBatch, err := rt.GetBatch(ctx, batch.ID)
	testutil.FailErr(t, "GetBatch failed", err)
	if gotBatch.Status != orchestration.TopologyBatchFailed {
		t.Fatalf("batch status = %q, want failed", gotBatch.Status)
	}
}
