package security

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerplan/plantest"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDelegationCloseoutWithoutSecurityEvidenceGatePending(t *testing.T) {
	stageEmptyHintsAndSuppressions(t)
	projectDir := t.TempDir()
	evidenceStore := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)

	sqlDB := testdbfixture.Open(t, "store.db")
	_ = testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)

	store, obligation := commitSecurityLanding(t, sqlDB, projectDir, "dep-closeout")
	checker := &scan.SecurityCloseoutChecker{
		Store:    store,
		Evidence: evidenceStore,
	}

	ctx := context.Background()
	testdbseed.InsertSession(t, sqlDB, "child-closeout", testdbseed.DefaultProjectID)
	_, err := sqlDB.ExecContext(ctx, `UPDATE sessions SET parent_session_id = 'sess-closeout' WHERE id = 'child-closeout'`)
	testutil.FailErr(t, "bind child-closeout parent", err)
	records := workerrecords.New(sqlDB)
	actor := workerrecords.Actor{SessionID: "sess-closeout", Role: workerrecords.RoleCoordinator, PrincipalID: "sess-closeout"}
	planned, err := records.CreateWorker(ctx, actor, workerrecords.Op{ID: "create-leg-1"}, workerrecords.WorkerSpec{
		Name:           "leg-1",
		AgentType:      "implementer",
		ScopeMode:      workerrecords.ScopeWrite,
		Brief:          "implement landed change",
		ChildSessionID: "child-closeout",
		BlueprintPath:  "plans/test.md",
		WorkflowWorkID: "leg-1",
		Criteria:       []string{"job:complete"},
	})
	testutil.FailErr(t, "CreateWorker", err)
	jobID := "job-closeout-plan"
	now := timelayout.Format(time.Now().UTC())
	_, err = sqlDB.ExecContext(
		ctx,
		`INSERT INTO worker_jobs (id, project_id, workspace_path, parent_session_id, child_session_id, agent_type, prompt, brief, execution_target, status, created_at)
		 VALUES (?, ?, ?, 'sess-closeout', 'child-closeout', 'implementer', 'fixture', 'fixture', 'local', 'complete', ?)`,
		jobID, testdbseed.DefaultProjectID, projectDir, now,
	)
	testutil.FailErr(t, "insert settled plan job", err)
	_, err = records.AdmitRound(ctx, actor, workerrecords.Op{ID: "round-leg-1"}, planned.WorkerID, workerrecords.RoundSpec{
		JobID: jobID, ExecutionTarget: "local",
	})
	testutil.FailErr(t, "AdmitRound", err)
	_, err = sqlDB.ExecContext(ctx, `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES ('res-closeout-1', ?, 'complete', ?)`, jobID, now)
	testutil.FailErr(t, "insert worker_results", err)
	_, err = db.New(sqlDB).SettleWorkerRoundByJob(ctx, db.SettleWorkerRoundByJobParams{SettledAt: db.NullString(now), JobID: jobID})
	testutil.FailErr(t, "SettleWorkerRoundByJob", err)
	_, err = db.New(sqlDB).RecordWorkerResultCriteriaOutcomes(ctx, db.RecordWorkerResultCriteriaOutcomesParams{
		CriteriaOutcomesJson: workerrecords.EncodeCriteriaOutcomes([]workerrecords.CriterionOutcome{{Criterion: "job:complete", Passed: true}}),
		ResultID:             "res-closeout-1",
	})
	testutil.FailErr(t, "RecordWorkerResultCriteriaOutcomes", err)
	_, err = sqlDB.ExecContext(ctx, `INSERT INTO landed_changes (id, worker_job_id, canonical_path, created_at) VALUES ('land-closeout-1', ?, ?, ?)`, jobID, projectDir, now)
	testutil.FailErr(t, "insert landed_changes", err)
	_, err = records.SetChange(ctx, actor, workerrecords.Op{ID: "land-leg-1"}, "res-closeout-1", workerrecords.Change{
		State: workerrecords.ChangeLanded, LandedChangeID: "land-closeout-1",
	})
	testutil.FailErr(t, "SetChange landed", err)

	planDeps := plantest.Deps(records)
	planDeps.Jobs = worker.NewSQLQueue(sqlDB, 1)
	planDeps.Security = checker
	planDeps.HostDataDir = func(context.Context, string) (string, error) { return projectDir, nil }
	planSvc := plantest.New(t, planDeps)
	verdict, err := planSvc.CheckSessionCloseout(ctx, "sess-closeout", projectDir)
	testutil.FailErr(t, "CheckSessionCloseout", err)
	if verdict.OK || verdict.Gap != workerplan.GapScanPending {
		t.Fatalf("expected a pending scan gate without evidence, got %+v", verdict)
	}

	completeSecurityLanding(t, store, evidenceStore, projectDir, obligation)

	verdict, err = planSvc.CheckSessionCloseout(ctx, "sess-closeout", projectDir)
	testutil.FailErr(t, "CheckSessionCloseout after ingest", err)
	if !verdict.OK {
		t.Fatalf("expected closeout pass after ingested evidence: %+v", verdict)
	}

	// MCP meta.guidance alone does not satisfy gate evidence (no JSONL anchors).
	metaOnly := evidence.Record{
		GateType:    string(evidence.GateTypeSecurity),
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts: map[string]any{
			"guidance": []api.ScanGuidanceSummary{{Code: "SCAN_SQL_INJECTION", Message: "from mcp"}},
		},
	}
	ok, _ := inspector.EvidenceAnchored(metaOnly)
	if ok {
		t.Fatal("MCP guidance-only record must not satisfy security gate anchors")
	}
}
