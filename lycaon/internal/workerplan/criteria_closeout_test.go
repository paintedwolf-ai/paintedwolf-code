package workerplan_test

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubGitDiff struct {
	uncommitted map[string]bool
	fromRef     map[string]bool
}

func (g stubGitDiff) HasUncommittedDiff(_ context.Context, _, path string) (bool, error) {
	return g.uncommitted[path], nil
}

func (g stubGitDiff) DiffFromRef(_ context.Context, _, _, path string) (bool, error) {
	return g.fromRef[path], nil
}

type stubSourceRuns struct {
	runs map[string][]workercompletion.WorkerInvocationReceipt
}

func (s stubSourceRuns) WorkerSourceRuns(_ context.Context, child string) ([]workercompletion.WorkerInvocationReceipt, error) {
	return s.runs[child], nil
}

type stubInspector struct {
	statuses map[string]*inspector.GateStatus
}

func (i stubInspector) RecordEvidence(_ context.Context, _, _ string, _ evidence.Record) error {
	return nil
}

func (i stubInspector) CheckGates(_ context.Context, _ string, _ []string, _ []string) (*inspector.GateCheckResult, error) {
	return &inspector.GateCheckResult{OK: true}, nil
}

func (i stubInspector) GetStatus(_ context.Context, runID, slot string) (*inspector.GateStatus, error) {
	return i.statuses[runID+"/"+slot], nil
}

type stubJobs struct {
	tasks map[string]*api.WorkerTask
}

func (j *stubJobs) Get(id string) (*api.WorkerTask, bool) {
	task, ok := j.tasks[id]
	return task, ok
}

func TestEvaluateCriteriaAndRoundDelivery(t *testing.T) {
	env := newPlanTestEnv(t)
	ctx := t.Context()

	if workerplan.NewGitDiffAdapter(nil) != nil {
		t.Fatal("expected nil adapter for nil git manager")
	}

	env.svc.GitDiff = stubGitDiff{
		uncommitted: map[string]bool{"pkg/uncommitted.go": true},
		fromRef:     map[string]bool{"pkg/committed.go": true},
	}
	env.svc.SourceRuns = stubSourceRuns{
		runs: map[string][]workercompletion.WorkerInvocationReceipt{
			"child-pass": {
				{Command: "go test ./pkg/...", Verdict: string(evidence.GateVerdictFailed)},
				{Command: "go test ./pkg/...", Verdict: string(evidence.GateVerdictPassed)},
			},
			"child-fail": {
				{Command: "go test ./pkg/...", Verdict: string(evidence.GateVerdictFailed)},
			},
		},
	}
	env.svc.Inspector = stubInspector{
		statuses: map[string]*inspector.GateStatus{
			"run-1/leg-insp": {
				Evidence: map[evidence.GateType]*evidence.Record{
					evidence.GateTypeVerify: {
						Artifacts: map[string]any{"command": "go test ./insp/...", "exit_code": float64(0)},
					},
				},
			},
		},
	}

	env.insertWorkflowRun(t, "run-1", "work")
	created, err := env.records.CreateWorker(ctx, env.coord, workerrecords.Op{ID: "crit-w"}, workerrecords.WorkerSpec{
		Name:           "impl-1",
		AgentType:      "implementer",
		ScopeMode:      workerrecords.ScopeWrite,
		WorkflowRunID:  "run-1",
		WorkflowWorkID: "leg-insp",
		BaseSHA:        "abc1234",
		Criteria: []string{
			"job:complete",
			"summary:present",
			"file:modified:pkg/report.go",
			"file:modified:pkg/uncommitted.go",
			"file:modified:pkg/committed.go",
			"test:pass:go test ./pkg/...",
			"test:pass:go test ./insp/...",
		},
	})
	testutil.FailErr(t, "create criteria worker", err)
	env.insertJobAndRound(t, created.WorkerID, "job-crit", false)

	outcomes := env.svc.EvaluateRoundDelivery(
		ctx,
		env.coord.SessionID,
		"job-crit",
		"child-pass",
		"/repo",
		true,
		"Implemented package changes.",
		nil,
		&api.WorkerChangeReport{ChangedPaths: []string{"pkg/report.go"}},
	)
	if len(outcomes) != 7 {
		t.Fatalf("outcomes len = %d, want 7", len(outcomes))
	}
	for _, o := range outcomes {
		if !o.Passed {
			t.Fatalf("expected criterion %s to pass, got %+v", o.Criterion, o)
		}
	}

	failed := env.svc.EvaluateRoundDelivery(
		ctx,
		env.coord.SessionID,
		"job-crit",
		"child-fail",
		"/repo",
		false,
		"   ",
		nil,
		nil,
	)
	if failed[0].Passed || failed[1].Passed || failed[2].Passed || failed[5].Passed {
		t.Fatalf("expected failures on incomplete round, got %+v", failed)
	}
}

func TestCheckSessionCloseoutAndCloseoutComplete(t *testing.T) {
	env := newPlanTestEnv(t)
	ctx := t.Context()
	jobs := &stubJobs{tasks: map[string]*api.WorkerTask{}}
	env.svc.Jobs = jobs

	const bpPath = ".lycaon/plans/closeout.md"
	env.insertWorkflowRun(t, "run-close", "work")
	created, err := env.records.CreateWorker(ctx, env.coord, workerrecords.Op{ID: "close-w"}, workerrecords.WorkerSpec{
		Name:           "impl-close",
		AgentType:      "implementer",
		ScopeMode:      workerrecords.ScopeWrite,
		WorkflowRunID:  "run-close",
		WorkflowWorkID: "leg-close",
		BlueprintPath:  bpPath,
		Files:          []string{"pkg/close.go"},
		Criteria:       []string{"job:complete", "summary:present", "file:modified:pkg/close.go"},
	})
	testutil.FailErr(t, "create closeout worker", err)

	cond := env.svc.CloseoutComplete()

	v, err := env.svc.CheckSessionCloseout(ctx, env.coord.SessionID, testdbseed.DefaultProjectID, "/repo")
	testutil.FailErr(t, "unstarted closeout", err)
	if v.OK || v.Code != "COORDINATOR_CRITERIA_UNMET" {
		t.Fatalf("expected unstarted closeout block, got %+v", v)
	}
	if ok, _ := cond(ctx, env.coord.SessionID); ok {
		t.Fatal("expected CloseoutComplete=false while unstarted")
	}

	env.insertJobAndRound(t, created.WorkerID, "job-c1", false)
	v, err = env.svc.CheckSessionCloseout(ctx, env.coord.SessionID, testdbseed.DefaultProjectID, "/repo")
	testutil.FailErr(t, "active round closeout", err)
	if v.OK {
		t.Fatalf("expected active round block, got %+v", v)
	}

	now := db.FormatTime(time.Now().UTC())
	_, err = env.sqlDB.ExecContext(ctx, `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES ('res-c1', 'job-c1', 'complete', ?)`, now)
	testutil.FailErr(t, "insert res-c1", err)
	_, err = db.New(env.sqlDB).SettleWorkerRoundByJob(ctx, db.SettleWorkerRoundByJobParams{SettledAt: db.NullString(now), JobID: "job-c1"})
	testutil.FailErr(t, "settle job-c1", err)

	_, err = db.New(env.sqlDB).RecordWorkerResultCriteriaOutcomes(ctx, db.RecordWorkerResultCriteriaOutcomesParams{
		CriteriaOutcomesJson: workerrecords.EncodeCriteriaOutcomes([]workerrecords.CriterionOutcome{
			{Criterion: "job:complete", Passed: true},
			{Criterion: "summary:present", Passed: true},
			{Criterion: "file:modified:pkg/close.go", Passed: false, Detail: "file was not modified"},
		}),
		ResultID: "res-c1",
	})
	testutil.FailErr(t, "record failing outcomes", err)
	v, err = env.svc.CheckSessionCloseout(ctx, env.coord.SessionID, testdbseed.DefaultProjectID, "/repo")
	testutil.FailErr(t, "unmet criterion closeout", err)
	if v.OK || v.ResultID != "res-c1" {
		t.Fatalf("expected unmet criterion block on res-c1, got %+v", v)
	}

	_, err = env.sqlDB.ExecContext(ctx, `INSERT INTO worker_jobs (
			id, project_id, parent_session_id, agent_type, status, prompt, brief, created_at
		) VALUES ('job-c2', ?, ?, 'implementer', 'complete', 'prompt', 'brief', ?)`,
		testdbseed.DefaultProjectID, env.coord.SessionID, now)
	testutil.FailErr(t, "insert job-c2", err)
	_, err = env.records.AdmitRound(ctx, env.coord, workerrecords.Op{ID: "round-c2"}, created.WorkerID, workerrecords.RoundSpec{
		JobID: "job-c2", ExecutionTarget: "local",
	})
	testutil.FailErr(t, "admit round-c2", err)
	_, err = env.sqlDB.ExecContext(ctx, `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES ('res-c2', 'job-c2', 'complete', ?)`, now)
	testutil.FailErr(t, "insert res-c2", err)
	_, err = db.New(env.sqlDB).SettleWorkerRoundByJob(ctx, db.SettleWorkerRoundByJobParams{SettledAt: db.NullString(now), JobID: "job-c2"})
	testutil.FailErr(t, "settle job-c2", err)
	_, err = db.New(env.sqlDB).RecordWorkerResultCriteriaOutcomes(ctx, db.RecordWorkerResultCriteriaOutcomesParams{
		CriteriaOutcomesJson: workerrecords.EncodeCriteriaOutcomes([]workerrecords.CriterionOutcome{
			{Criterion: "job:complete", Passed: true},
			{Criterion: "summary:present", Passed: true},
			{Criterion: "file:modified:pkg/close.go", Passed: true},
		}),
		ResultID: "res-c2",
	})
	testutil.FailErr(t, "record passing outcomes", err)

	jobs.tasks["job-c2"] = &api.WorkerTask{
		ID:                   "job-c2",
		Status:               api.WorkerStatusComplete,
		MergeStatus:          api.WorkerMergeStatusPending,
		WorkspaceOverlayPath: "/tmp/overlay-c2",
	}
	v, err = env.svc.CheckSessionCloseout(ctx, env.coord.SessionID, testdbseed.DefaultProjectID, "/repo")
	testutil.FailErr(t, "open overlay closeout", err)
	if v.OK {
		t.Fatalf("expected unlanded overlay to block closeout, got %+v", v)
	}

	_, err = env.records.SetChange(ctx, env.coord, workerrecords.Op{ID: "shelve-c2"}, "res-c2", workerrecords.Change{
		State:        workerrecords.ChangeShelved,
		ShelveReason: "defer",
	})
	testutil.FailErr(t, "shelve res-c2", err)
	v, err = env.svc.CheckSessionCloseout(ctx, env.coord.SessionID, testdbseed.DefaultProjectID, "/repo")
	testutil.FailErr(t, "shelved file:modified closeout", err)
	if v.OK {
		t.Fatalf("expected shelved file:modified result to block closeout, got %+v", v)
	}

	_, err = env.sqlDB.ExecContext(ctx, `INSERT INTO landed_changes (id, worker_job_id, canonical_path, created_at) VALUES ('land-c2', 'job-c2', '/repo', ?)`, now)
	testutil.FailErr(t, "insert landed_changes", err)
	_, err = env.records.SetChange(ctx, env.coord, workerrecords.Op{ID: "land-c2-op"}, "res-c2", workerrecords.Change{
		State:          workerrecords.ChangeLanded,
		LandedChangeID: "land-c2",
	})
	testutil.FailErr(t, "mark landed", err)
	jobs.tasks["job-c2"].MergeStatus = api.WorkerMergeStatusMerged
	jobs.tasks["job-c2"].WorkspaceOverlayPath = ""

	v, err = env.svc.CheckSessionCloseout(ctx, env.coord.SessionID, testdbseed.DefaultProjectID, "/repo")
	testutil.FailErr(t, "settled closeout", err)
	if !v.OK {
		t.Fatalf("expected closeout OK, got %+v", v)
	}
	if gotRun := env.svc.SettledWorkflowRunID(ctx, env.coord.SessionID); gotRun != "run-close" {
		t.Fatalf("SettledWorkflowRunID = %q, want run-close", gotRun)
	}
	if ok, err := cond(ctx, env.coord.SessionID); err != nil || !ok {
		t.Fatalf("CloseoutComplete = (%v, %v), want (true, nil)", ok, err)
	}
}
