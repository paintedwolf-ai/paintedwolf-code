package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/scan/obligation"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

type staticHead struct{ sha string }

func (s staticHead) HeadSHA(context.Context, string) (string, error) { return s.sha, nil }

func commitRequiredLanding(t *testing.T, sqlDB db.Handle, projectDir, delegationID string) (*scan.SQLStore, api.CodeScan) {
	t.Helper()
	ctx := t.Context()
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-1", "session-1", testdbseed.DefaultProjectID)
	jobID := uuid.NewString()
	workerStore := worker.NewSQLStore(sqlDB)
	testutil.FailErr(t, "InsertTask", workerStore.InsertTask(ctx, api.WorkerTask{
		ID:              jobID,
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   projectDir,
		DelegationID:    delegationID,
		WorkflowRunID:   "run-1",
		AgentType:       "implementer",
		Prompt:          "fixture",
		Brief:           "fixture",
		ExecutionTarget: api.ExecutionTargetLocal,
		Status:          api.WorkerStatusComplete,
	}))
	bound, err := workerStore.SetWorkerWorkspace(ctx, jobID, filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches", jobID), testbaseline.Durable(t, sqlDB, jobID, t.TempDir()))
	testutil.FailErr(t, "SetWorkerWorkspace", err)
	if !bound {
		t.Fatal("expected workspace root CAS to win on a fresh job")
	}
	testutil.FailErr(t, "SetMergeStatus", workerStore.SetMergeStatus(ctx, jobID, api.WorkerMergeStatusPending))
	token, claimed, err := workerStore.BeginMergeApply(ctx, jobID)
	testutil.FailErr(t, "BeginMergeApply", err)
	if !claimed {
		t.Fatal("expected merge apply claim on a pending overlay")
	}
	manifest, executionFingerprint, manifestErr := scancatalog.ExecutionManifest(testScannerContract("sast-one"))
	testutil.FailErr(t, "ExecutionManifest", manifestErr)
	plan := obligation.Plan{
		ID:                   uuid.NewString(),
		ScanID:               uuid.NewString(),
		AssessmentID:         uuid.NewString(),
		WorkerJobID:          jobID,
		CanonicalPath:        projectDir,
		DelegationID:         delegationID,
		WorkflowRunID:        "run-1",
		ScannerID:            "sast-one",
		ChangedPaths:         []string{"main.go"},
		ExecutionManifest:    manifest,
		ExecutionFingerprint: executionFingerprint,
		FingerprintScheme:    api.ScanFingerprintScheme,
		PathScoped:           true,
		Required:             true,
		CreatedAt:            time.Now().UTC(),
	}
	testutil.FailErr(t, "CommitPromotion", workerStore.CommitPromotion(ctx, jobID, token, worker.PromotionCommit{Plan: plan}))

	scanStore := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, scanStore, staticHead{sha: "head-1"})
	testutil.FailErr(t, "PublishPending", coord.PublishPending(ctx, plan.ScanID))
	rec, err := scanStore.Get(ctx, plan.ScanID)
	testutil.FailErr(t, "Get scan", err)
	if rec == nil {
		t.Fatal("scan obligation missing")
	}
	return scanStore, *rec
}

func completeSecurityEvidence(
	t *testing.T,
	store *scan.SQLStore,
	evidenceStore inspector.EvidenceStore,
	projectDir string,
	rec api.CodeScan,
	result *scanoutput.Result,
) {
	t.Helper()
	if result == nil {
		result = &scanoutput.Result{}
	}
	claimed := claimScan(t, store, rec.ID)
	won, err := store.MarkComplete(t.Context(), claimed, result)
	testutil.FailErr(t, "MarkComplete", err)
	if !won {
		t.Fatalf("scan %s was not open for completion", rec.ID)
	}
	ins := inspector.NewSimpleInspector(evidenceStore)
	ins.ProjectDir = func(context.Context, string) (string, error) { return projectDir, nil }
	ing := &scan.IngesterImpl{
		Inspector: ins,
		Module:    scancfg.DefaultModuleConfig(),
		Budget:    scancfg.NewFindingBudget(scancfg.DefaultAgentBudget()),
		BlockOn:   []string{"error"},
	}
	evidenceRec, err := ing.Ingest(t.Context(), scan.ScanSourceRegistry, result, completeIngestMeta(t, scan.IngestMeta{
		ScanID:           rec.ID,
		ProjectDir:       projectDir,
		HeadSHA:          rec.HeadSHA,
		SourceSnapshotID: rec.SourceSnapshotID,
		DelegationID:     rec.DelegationID,
		TaskID:           api.ScanTaskID(rec.ID),
		Scanner:          testScannerContract(rec.ScannerID),
		Categories:       rec.Categories,
	}))
	testutil.FailErr(t, "Ingest", err)
	testutil.FailErr(t, "SaveIngest", store.SaveIngest(t.Context(), rec.ID, evidenceRec))
}
