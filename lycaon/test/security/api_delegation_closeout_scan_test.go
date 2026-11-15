package security

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDelegationCloseoutWithoutSecurityEvidenceGatePending(t *testing.T) {
	stageEmptyHintsAndSuppressions(t)
	projectDir := t.TempDir()
	evidenceStore := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)

	sqlDB := testdbfixture.Open(t, "store.db")
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)

	store, obligation := commitSecurityLanding(t, sqlDB, projectDir, "dep-closeout")
	checker := &scan.SecurityCloseoutChecker{
		Store:    store,
		Evidence: evidenceStore,
	}

	ctx := context.Background()
	records := workerrecords.New(sqlDB)
	actor := workerrecords.Actor{SessionID: "sess-closeout", Role: workerrecords.RoleCoordinator, PrincipalID: "sess-closeout"}
	planned, err := records.CreatePlannedWorker(ctx, actor, workerrecords.CreatePlannedWorkerInput{
		ProjectID:      testdbseed.DefaultProjectID,
		RootID:         rootID,
		Name:           "leg-1",
		AgentType:      "implementer",
		ScopeMode:      workerrecords.ScopeWrite,
		Brief:          "implement landed change",
		BlueprintPath:  "plans/test.md",
		WorkflowWorkID: "leg-1",
		Criteria:       []string{"job:complete"},
	})
	testutil.FailErr(t, "CreatePlannedWorker", err)
	testdbseed.InsertSession(t, sqlDB, "child-closeout", testdbseed.DefaultProjectID)
	jobID := "job-closeout-plan"
	_, err = sqlDB.ExecContext(
		ctx,
		`INSERT INTO worker_jobs (id, project_id, workspace_path, parent_session_id, child_session_id, agent_type, prompt, brief, execution_target, status, created_at)
		 VALUES (?, ?, ?, 'sess-closeout', 'child-closeout', 'implementer', 'fixture', 'fixture', 'local', 'complete', datetime('now'))`,
		jobID, testdbseed.DefaultProjectID, projectDir,
	)
	testutil.FailErr(t, "insert settled plan job", err)
	_, err = records.StartPlannedRound(ctx, actor, workerrecords.StartPlannedRoundInput{
		WorkerID:       planned.ID,
		ChildSessionID: "child-closeout",
		JobID:          jobID,
	})
	testutil.FailErr(t, "StartPlannedRound", err)
	_, err = records.RecordDelivery(ctx, actor, workerrecords.RecordDeliveryInput{
		WorkerID:    planned.ID,
		JobID:       jobID,
		ResultID:    "res-closeout-1",
		Delivered:   true,
		ChangeState: workerrecords.ChangeLanded,
		CriteriaOutcomes: []workerrecords.CriterionOutcome{
			{Criterion: "job:complete", Passed: true},
		},
	})
	testutil.FailErr(t, "RecordDelivery", err)

	planSvc := &workerplan.Service{
		Records:     records,
		Security:    checker,
		HostDataDir: func(context.Context, string) (string, error) { return projectDir, nil },
	}
	verdict, err := planSvc.CheckSessionCloseout(ctx, "sess-closeout", testdbseed.DefaultProjectID, projectDir)
	testutil.FailErr(t, "CheckSessionCloseout", err)
	if verdict.OK || verdict.GatePending == nil {
		t.Fatalf("expected gate_pending without evidence, got %+v", verdict)
	}

	completeSecurityLanding(t, store, evidenceStore, projectDir, obligation)

	verdict, err = planSvc.CheckSessionCloseout(ctx, "sess-closeout", testdbseed.DefaultProjectID, projectDir)
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
