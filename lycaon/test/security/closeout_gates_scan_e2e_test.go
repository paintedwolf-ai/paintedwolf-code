package security

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/scan/obligation"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerplan/plantest"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCloseoutGatesPassedAnchoredSecurityEvidence(t *testing.T) {
	// Isolate the test from device hint and suppression files.
	stageEmptyHintsAndSuppressions(t)
	projectDir := t.TempDir()
	testutil.FailErr(t, "write landed source fixture", os.WriteFile(
		filepath.Join(projectDir, "main.go"), []byte("package main\n"), 0o600))
	evidenceStore := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)

	sqlDB := testdbfixture.Open(t, "store.db")
	_ = testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-closeout", "sess-closeout", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, sqlDB, "child-closeout", testdbseed.DefaultProjectID)
	_, err := sqlDB.ExecContext(context.Background(), `UPDATE sessions SET parent_session_id = 'sess-closeout' WHERE id = 'child-closeout'`)
	testutil.FailErr(t, "bind child-closeout parent", err)

	store := scan.NewSQLStore(sqlDB)
	coord := scantest.Coordinator(t, store, staticHeadSHA{sha: "closeout-head"})
	proactive := scancfg.DefaultGatesConfig().Gates.ProactiveCategories

	records := workerrecords.New(sqlDB)
	actor := workerrecords.Actor{SessionID: "sess-closeout", Role: workerrecords.RoleCoordinator, PrincipalID: "sess-closeout"}
	planned, err := records.CreateWorker(context.Background(), actor, workerrecords.Op{ID: "create-leg-1"}, workerrecords.WorkerSpec{
		Name:           "leg-1",
		AgentType:      "implementer",
		ScopeMode:      workerrecords.ScopeWrite,
		Brief:          "Verify landed source",
		ChildSessionID: "child-closeout",
		BlueprintPath:  "plans/test.md",
		WorkflowRunID:  "run-closeout",
		WorkflowWorkID: "leg-1",
	})
	testutil.FailErr(t, "CreateWorker", err)
	now := timelayout.Format(time.Now().UTC())
	_, err = sqlDB.ExecContext(
		context.Background(),
		`INSERT INTO worker_jobs (id, project_id, workspace_path, parent_session_id, child_session_id, agent_type, prompt, brief, execution_target, status, created_at)
		 VALUES ('job-leg-1', ?, ?, 'sess-closeout', 'child-closeout', 'implementer', 'fixture', 'fixture', 'local', 'complete', ?)`,
		testdbseed.DefaultProjectID, projectDir, now,
	)
	testutil.FailErr(t, "insert settled plan job", err)
	_, err = records.AdmitRound(context.Background(), actor, workerrecords.Op{ID: "round-leg-1"}, planned.WorkerID, workerrecords.RoundSpec{
		JobID: "job-leg-1", ExecutionTarget: "local",
	})
	testutil.FailErr(t, "AdmitRound", err)
	_, err = sqlDB.ExecContext(context.Background(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES ('res-leg-1', 'job-leg-1', 'complete', ?)`, now)
	testutil.FailErr(t, "insert worker_results", err)
	_, err = db.New(sqlDB).SettleWorkerRoundByJob(context.Background(), db.SettleWorkerRoundByJobParams{SettledAt: db.NullString(now), JobID: "job-leg-1"})
	testutil.FailErr(t, "SettleWorkerRoundByJob", err)
	_, err = sqlDB.ExecContext(context.Background(), `INSERT INTO landed_changes (id, worker_job_id, canonical_path, created_at) VALUES ('land-leg-1', 'job-leg-1', ?, ?)`, projectDir, now)
	testutil.FailErr(t, "insert landed_changes", err)
	_, err = records.SetChange(context.Background(), actor, workerrecords.Op{ID: "land-leg-1-op"}, "res-leg-1", workerrecords.Change{
		State: workerrecords.ChangeLanded, LandedChangeID: "land-leg-1",
	})
	testutil.FailErr(t, "SetChange landed", err)
	planDeps := plantest.Deps(records)
	planDeps.Jobs = worker.NewSQLServices(sqlDB, 1).Queue
	planSvc := plantest.New(t, planDeps)

	verifyRec := evidence.Record{
		GateType:    string(evidence.GateTypeVerify),
		Slot:        "leg-1",
		RunID:       "sess-closeout",
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts: map[string]any{
			"exit_code": 0,
			"command":   "go test ./...",
		},
	}
	if err := evidenceStore.Append(context.Background(), projectDir, verifyRec); err != nil {
		testutil.FailErr(t, "evidenceStore.Append failed", err)
	}

	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		SessionWorkIDs:          planSvc.SessionWorkIDs,
		Evidence:                conditions.StoreEvidenceReader{Store: evidenceStore},
		PlanCloseout:            planSvc.CloseoutComplete,
		ScanLedger:              store.Reads,
		SourceSnapshots:         coord.SnapshotStore(),
		ScanProactiveCategories: proactive,
	})
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	created := commitRequiredSecurityLanding(t, sqlDB, coord, store, projectDir, "dep-closeout")
	claimed, err := store.Runs.ClaimNext(t.Context())
	testutil.FailErr(t, "claim scan", err)
	if claimed.ID != created.ID {
		t.Fatalf("claimed scan %q want %q", claimed.ID, created.ID)
	}
	if _, err := store.Runs.MarkComplete(context.Background(), claimed, &scanoutput.Result{}); err != nil {
		testutil.FailErr(t, "store.MarkComplete failed", err)
	}
	completed, err := store.Reads.Get(t.Context(), created.ID)
	testutil.FailErr(t, "get completed scan", err)
	ec := conditions.EvalContext{
		Ctx:        context.Background(),
		SessionID:  "sess-closeout",
		ProjectDir: projectDir,
	}
	ok, err := reg.Evaluate("closeout_gates_passed", ec)
	if err != nil || ok {
		t.Fatalf("closeout without security evidence = %v err=%v want false", ok, err)
	}
	ok, err = reg.Evaluate("evidence_passed:security", ec)
	if err != nil || ok {
		t.Fatalf("evidence_passed:security without ingest = %v err=%v want false", ok, err)
	}

	ins := inspector.NewSimpleInspector(evidenceStore)
	ins.ProjectDir = func(context.Context, string) (string, error) { return projectDir, nil }
	ing := &scan.IngesterImpl{
		Inspector: ins,
		Module:    scancfg.DefaultModuleConfig(),
		Budget:    scancfg.NewFindingBudget(scancfg.DefaultAgentBudget()),
		BlockOn:   []string{"error"},
	}
	rec, err := ing.Ingest(context.Background(), scan.ScanSourceRegistry, &scanoutput.Result{}, scan.IngestMeta{
		ScanID:           created.ID,
		ProjectDir:       projectDir,
		HeadSHA:          "closeout-head",
		DelegationID:     "dep-closeout",
		SourceSnapshotID: created.SourceSnapshotID,
		AssessmentID:     completed.AssessmentID, CoverageStatus: completed.CoverageStatus,
		ExecutionManifest: completed.ExecutionManifest, ExecutionFingerprint: completed.ExecutionFingerprint,
		TaskID:     api.ScanTaskID(created.ID),
		Scanner:    securityScannerContract(created.ScannerID),
		Categories: proactive,
	})
	testutil.FailErr(t, "ing.Ingest failed", err)
	if err := store.Findings.SaveIngest(context.Background(), created.ID, rec); err != nil {
		testutil.FailErr(t, "store.SaveIngest failed", err)
	}

	ok, err = reg.Evaluate("evidence_passed:security", ec)
	if err != nil || !ok {
		_, reason := inspector.SecurityEvidenceMatchesSnapshot(rec, created.SourceSnapshotID)
		t.Fatalf("evidence_passed:security after ingest = %v err=%v anchor=%q", ok, err, reason)
	}
	ok, err = reg.Evaluate("closeout_gates_passed", ec)
	if err != nil || !ok {
		t.Fatalf("closeout_gates_passed after anchored security = %v err=%v", ok, err)
	}

	if proof, ok := rec.Artifacts["engine_proof"].(scan.EngineProof); !ok || !proof.Deterministic.Completed {
		t.Fatal("expected engine_proof on security evidence artifacts")
	}
}

func commitRequiredSecurityLanding(
	t *testing.T,
	sqlDB db.Handle,
	coord *scan.CoordinatorImpl,
	store *scan.SQLStore,
	projectDir string,
	delegationID string,
) *api.CodeScan {
	t.Helper()
	ctx := t.Context()
	jobID := uuid.NewString()
	workerStore := worker.NewSQLStore(sqlDB)
	testutil.FailErr(t, "InsertTask", workerStore.InsertTask(ctx, api.WorkerTask{
		Prompt: "fixture",
		Brief:  "fixture",
		ID:     jobID, ProjectID: testdbseed.DefaultProjectID, WorkspacePath: projectDir,
		ParentSessionID: "sess-closeout",
		DelegationID:    delegationID, WorkflowRunID: "run-closeout", AgentType: "implementer",
		ExecutionTarget: api.ExecutionTargetLocal, Status: api.WorkerStatusComplete,
	}))
	branch := enginepaths.JobBranchDir(enginepaths.WorkerBranchesRootUnder(testbaseline.DataDir(t, sqlDB)), projectDir, jobID)
	bound, err := workerStore.SetWorkerWorkspace(ctx, jobID, branch, testbaseline.Durable(t, "root", sqlDB, jobID, t.TempDir()))
	testutil.FailErr(t, "SetWorkerWorkspace", err)
	if !bound {
		t.Fatal("worker workspace binding did not win")
	}
	testutil.FailErr(t, "SetMergeStatus", workerStore.SetMergeStatus(ctx, jobID, api.WorkerMergeStatusPending))
	token, claimed, err := workerStore.BeginMergeApply(ctx, jobID)
	testutil.FailErr(t, "BeginMergeApply", err)
	if !claimed {
		t.Fatal("merge apply was not claimed")
	}
	manifest, fingerprint, err := scancatalog.ExecutionManifest(securityScannerContract("sast-one"))
	testutil.FailErr(t, "build scanner execution manifest", err)
	// The first assessment needs a full scan because no incremental baseline exists.
	plan := obligation.Plan{
		ID: uuid.NewString(), ScanID: uuid.NewString(), AssessmentID: uuid.NewString(), WorkerJobID: jobID,
		CanonicalPath: projectDir, SessionID: "sess-closeout", DelegationID: delegationID, WorkflowRunID: "run-closeout",
		ScannerID: "sast-one", ChangedPaths: []string{"main.go"},
		ExecutionManifest: manifest, ExecutionFingerprint: fingerprint, FingerprintScheme: api.ScanFingerprintScheme,
		Required: true, CreatedAt: time.Now().UTC(),
	}
	testutil.FailErr(t, "CommitPromotion", workerStore.CommitPromotion(ctx, jobID, token, worker.PromotionCommit{Plan: plan}))
	testutil.FailErr(t, "PublishPending", coord.PublishPending(ctx, plan.ScanID))
	rec, err := store.Reads.Get(ctx, plan.ScanID)
	testutil.FailErr(t, "Get landed scan", err)
	if rec == nil {
		t.Fatal("landed scan obligation missing")
	}
	return rec
}
