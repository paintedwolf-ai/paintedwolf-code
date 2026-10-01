package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/scan/obligation"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type failingPromotionSink struct{}

func (failingPromotionSink) SourceChanged(context.Context, api.SourceChangesEvent) error {
	return errors.New("source event rejected")
}

func (failingPromotionSink) SourceChangedTx(context.Context, *sql.Tx, api.SourceChangesEvent) error {
	return errors.New("source event rejected")
}

func (failingPromotionSink) Deliver() {}

func obligationTestStore(t *testing.T) *SQLStore {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-1", "workflow-session-1", testdbseed.DefaultProjectID)
	return NewSQLStore(sqlDB)
}

// insertApplyingWorker seeds a complete worker whose overlay apply is claimed,
// returning the merge-apply claim token that commits present.
func insertApplyingWorker(t *testing.T, store *SQLStore, jobID, projectDir string) string {
	t.Helper()
	testutil.FailErr(t, "InsertTask", store.InsertTask(t.Context(), api.WorkerTask{
		ID: jobID, ProjectID: testdbseed.DefaultProjectID, WorkspacePath: projectDir,
		DelegationID: "dep-1", WorkflowRunID: "run-1", AgentType: "implementer",
		Prompt: "fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal, Status: api.WorkerStatusComplete,
	}))
	bound, err := store.SetWorkerWorkspace(t.Context(), jobID, filepath.Join(testbaseline.DataDir(t, store.db), "worker-branches", jobID), testbaseline.Durable(t, store.db, jobID, t.TempDir()))
	testutil.FailErr(t, "SetWorkerWorkspace", err)
	if !bound {
		t.Fatal("expected workspace root CAS to win on a fresh job")
	}
	testutil.FailErr(t, "SetMergeStatus", store.SetMergeStatus(t.Context(), jobID, api.WorkerMergeStatusPending))
	token, ok, err := store.BeginMergeApply(t.Context(), jobID)
	testutil.FailErr(t, "BeginMergeApply", err)
	if !ok {
		t.Fatal("expected merge apply claim on a pending overlay")
	}
	return token
}

func testObligationPlan(jobID, projectDir string) obligation.Plan {
	return obligation.Plan{
		ID: "landing-1", ScanID: "scan-1", AssessmentID: "assessment-1", WorkerJobID: jobID, CanonicalPath: projectDir,
		DelegationID: "dep-1", WorkflowRunID: "run-1", ScannerID: "sast-one",
		ChangedPaths: []string{"src/main.go", "src/old.go"}, DeletedPaths: []string{"src/old.go"},
		ExecutionManifest: api.ScanExecutionManifest{
			SchemaVersion: "v1", ScannerID: "sast-one", Engine: "test", Driver: "test",
			ScopeKind: "source_driver", DefinitionFingerprint: strings.Repeat("a", 64),
			FingerprintScheme: api.ScanFingerprintScheme,
		},
		ExecutionFingerprint: strings.Repeat("b", 64), FingerprintScheme: api.ScanFingerprintScheme,
		PathScoped: true, Required: true,
		CreatedAt: time.Date(2026, 8, 11, 1, 2, 3, 4, time.UTC),
	}
}

func TestCommitPromotionIsOneDurableTransaction(t *testing.T) {
	store := obligationTestStore(t)
	projectDir := t.TempDir()
	token := insertApplyingWorker(t, store, "job-1", projectDir)
	seedWorkerReservation(t, store, "job-1", "session-1")
	plan := testObligationPlan("job-1", projectDir)

	testutil.FailErr(t, "CommitPromotion", store.CommitPromotion(t.Context(), "job-1", token, PromotionCommit{Plan: plan}))
	// Commit replay is idempotent.
	testutil.FailErr(t, "CommitPromotion replay", store.CommitPromotion(t.Context(), "job-1", token, PromotionCommit{Plan: plan}))

	var mergeStatus, landingID, scanID, changedJSON, deletedJSON string
	var required int
	err := store.db.QueryRowContext(t.Context(), `
		SELECT w.merge_status, l.id, IFNULL(l.scan_id, ''), l.changed_paths_json,
		       l.deleted_paths_json, l.scan_required
		FROM worker_jobs w JOIN landed_changes l ON l.worker_job_id = w.id
		WHERE w.id = ?
	`, "job-1").Scan(&mergeStatus, &landingID, &scanID, &changedJSON, &deletedJSON, &required)
	testutil.FailErr(t, "read atomic state", err)
	if mergeStatus != string(api.WorkerMergeStatusMerged) || landingID != plan.ID || scanID != plan.ScanID || required != 1 {
		t.Fatalf("atomic state = merge=%q landing=%q scan=%q required=%d", mergeStatus, landingID, scanID, required)
	}
	if changedJSON != `["src/main.go","src/old.go"]` || deletedJSON != `["src/old.go"]` {
		t.Fatalf("attribution = changed %s deleted %s", changedJSON, deletedJSON)
	}
	assertReservationCount(t, store, "session-1", 0)
	var categories, scannerID, status, snapshotID, pathsJSON string
	err = store.db.QueryRowContext(t.Context(), `
		SELECT categories_json, scanner_id, status, source_snapshot_id, paths_json
		FROM code_scans WHERE id = ?
	`, plan.ScanID).Scan(&categories, &scannerID, &status, &snapshotID, &pathsJSON)
	testutil.FailErr(t, "read scan", err)
	if categories != `["sast"]` || scannerID != "sast-one" || status != "pending" || snapshotID != api.SourceSnapshotWarming {
		t.Fatalf("scan = categories=%s scanner=%q status=%q snapshot=%q", categories, scannerID, status, snapshotID)
	}
	var firstTargets []string
	testutil.FailErr(t, "decode first targets", json.Unmarshal([]byte(pathsJSON), &firstTargets))
	if len(firstTargets) != 1 || firstTargets[0] != "src/main.go" {
		t.Fatalf("first targets = %#v", firstTargets)
	}

	secondToken := insertApplyingWorker(t, store, "job-2", projectDir)
	second := testObligationPlan("job-2", projectDir)
	second.ID = "landing-2"
	second.ScanID = "scan-2"
	second.AssessmentID = "assessment-2"
	second.ChangedPaths = []string{"lib/other.go"}
	second.DeletedPaths = nil
	testutil.FailErr(t, "CommitPromotion second", store.CommitPromotion(t.Context(), "job-2", secondToken, PromotionCommit{Plan: second}))
	var secondPathsJSON string
	testutil.FailErr(t, "read cumulative targets", store.db.QueryRowContext(t.Context(), `
		SELECT paths_json FROM code_scans WHERE id = ?
	`, second.ScanID).Scan(&secondPathsJSON))
	var secondTargets []string
	testutil.FailErr(t, "decode cumulative targets", json.Unmarshal([]byte(secondPathsJSON), &secondTargets))
	if len(secondTargets) != 2 || secondTargets[0] != "lib/other.go" || secondTargets[1] != "src/main.go" {
		t.Fatalf("second scan does not cover cumulative landed paths: %#v", secondTargets)
	}
}

func TestCommitPromotionRollsBackMergeWhenObligationInsertFails(t *testing.T) {
	store := obligationTestStore(t)
	projectDir := t.TempDir()
	token := insertApplyingWorker(t, store, "job-1", projectDir)
	seedWorkerReservation(t, store, "job-1", "session-1")
	testutil.FailErr(t, "seed conflicting scan", execWorkerSQL(t, store, `
		INSERT INTO code_scans(id, canonical_path, categories_json, status, created_at, reuse_key)
		VALUES ('scan-1', ?, '[]', 'pending', ?, 'scan-1')
	`, projectDir, db.FormatTime(time.Now().UTC())))

	err := store.CommitPromotion(t.Context(), "job-1", token, PromotionCommit{Plan: testObligationPlan("job-1", projectDir)})
	if err == nil {
		t.Fatal("expected obligation insert failure")
	}
	var mergeStatus string
	testutil.FailErr(t, "read merge status", store.db.QueryRowContext(t.Context(), `
		SELECT merge_status FROM worker_jobs WHERE id = 'job-1'
	`).Scan(&mergeStatus))
	if mergeStatus != string(api.WorkerMergeStatusApplying) {
		t.Fatalf("merge status = %q; transaction partially committed", mergeStatus)
	}
	var landings int
	testutil.FailErr(t, "count landings", store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM landed_changes`).Scan(&landings))
	if landings != 0 {
		t.Fatalf("landings = %d; transaction partially committed", landings)
	}
	assertReservationCount(t, store, "session-1", 1)
}

func TestCommitPromotionPersistsPolicyFailure(t *testing.T) {
	store := obligationTestStore(t)
	projectDir := t.TempDir()
	token := insertApplyingWorker(t, store, "job-1", projectDir)
	plan := testObligationPlan("job-1", projectDir)
	plan.ScannerID = ""
	plan.InitialFailure = "no selected static-analysis scanner is runnable"

	testutil.FailErr(t, "CommitPromotion", store.CommitPromotion(t.Context(), "job-1", token, PromotionCommit{Plan: plan}))
	var status, failure string
	testutil.FailErr(t, "read failed obligation", store.db.QueryRowContext(t.Context(), `
		SELECT status, error FROM code_scans WHERE id = ?
	`, plan.ScanID).Scan(&status, &failure))
	if status != "failed" || failure != plan.InitialFailure {
		t.Fatalf("failed obligation = status %q error %q", status, failure)
	}
}

func TestCommitPromotionRollsBackAllDurableConsequencesWhenSourceEventFails(t *testing.T) {
	store := obligationTestStore(t)
	projectDir := t.TempDir()
	token := insertApplyingWorker(t, store, "job-1", projectDir)
	t.Cleanup(sourcefeed.Bind(failingPromotionSink{}))

	err := store.CommitPromotion(t.Context(), "job-1", token, PromotionCommit{
		Plan: testObligationPlan("job-1", projectDir),
		Changes: []sourcefeed.Change{{
			ProjectID: testdbseed.DefaultProjectID, WorkspaceID: "workspace-1",
			WorkspaceKind: api.SourceWorkspaceKindProject,
			RootID:        "root-1", Path: "src/main.go",
			Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "source event rejected") {
		t.Fatalf("commit error = %v", err)
	}
	assertApplyingWithoutLanding(t, store, "job-1")
}

func TestCommitPromotionRollsBackWhenAnyChildTransitionFails(t *testing.T) {
	store := obligationTestStore(t)
	projectDir := t.TempDir()
	token := insertApplyingWorker(t, store, "job-1", projectDir)

	err := store.CommitPromotion(t.Context(), "job-1", token, PromotionCommit{
		Plan:          testObligationPlan("job-1", projectDir),
		ChildStatuses: []MergeStatusUpdate{{JobID: "missing-child", Status: api.WorkerMergeStatusPending}},
	})
	if err == nil || !strings.Contains(err.Error(), "missing-child") {
		t.Fatalf("commit error = %v", err)
	}
	assertApplyingWithoutLanding(t, store, "job-1")
}

func assertApplyingWithoutLanding(t *testing.T, store *SQLStore, jobID string) {
	t.Helper()
	var mergeStatus string
	testutil.FailErr(t, "read merge status", store.db.QueryRowContext(t.Context(), `
		SELECT merge_status FROM worker_jobs WHERE id = ?`, jobID).Scan(&mergeStatus))
	if mergeStatus != string(api.WorkerMergeStatusApplying) {
		t.Fatalf("merge status = %q; transaction partially committed", mergeStatus)
	}
	var landings int
	testutil.FailErr(t, "count landings", store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM landed_changes WHERE worker_job_id = ?`, jobID).Scan(&landings))
	if landings != 0 {
		t.Fatalf("landings = %d; transaction partially committed", landings)
	}
}

func TestSetMergeStatusesRollsBackTheWholeOverlayTree(t *testing.T) {
	store := obligationTestStore(t)
	testutil.FailErr(t, "insert parent", store.InsertTask(t.Context(), api.WorkerTask{
		ID: "parent", ProjectID: testdbseed.DefaultProjectID,
		AgentType: "implementer", Status: api.WorkerStatusComplete,
		ExecutionTarget: api.ExecutionTargetLocal, Prompt: "fixture", Brief: "fixture",
	}))
	testutil.FailErr(t, "set parent pending", store.SetMergeStatus(t.Context(), "parent", api.WorkerMergeStatusPending))

	err := store.SetMergeStatuses(t.Context(), []MergeStatusUpdate{
		{JobID: "parent", Status: api.WorkerMergeStatusRejected},
		{JobID: "missing-child", Status: api.WorkerMergeStatusOrphaned},
	})
	if err == nil {
		t.Fatal("overlay-tree transition unexpectedly succeeded")
	}
	var status string
	testutil.FailErr(t, "read parent status", store.db.QueryRowContext(t.Context(), `
		SELECT merge_status FROM worker_jobs WHERE id = 'parent'`).Scan(&status))
	if status != string(api.WorkerMergeStatusPending) {
		t.Fatalf("parent status = %q; overlay tree partially committed", status)
	}
}

func execWorkerSQL(t *testing.T, store *SQLStore, query string, args ...any) error {
	t.Helper()
	_, err := store.db.ExecContext(t.Context(), query, args...)
	return err
}

func seedWorkerReservation(t *testing.T, store *SQLStore, jobID, sessionID string) {
	t.Helper()
	testdbseed.InsertSession(t, store.db, sessionID, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "bind worker session", execWorkerSQL(t, store, `
		UPDATE worker_jobs SET parent_session_id = ? WHERE id = ?`, sessionID, jobID))
	testutil.FailErr(t, "insert worker reservation", execWorkerSQL(t, store, `
		INSERT INTO call_reservations(path, agent, session_id, created_at)
		VALUES (?, ?, ?, ?)`, "src/main.go", jobID, sessionID, db.FormatTime(time.Now().UTC())))
}

func assertReservationCount(t *testing.T, store *SQLStore, sessionID string, want int) {
	t.Helper()
	var got int
	testutil.FailErr(t, "count worker reservations", store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM call_reservations WHERE session_id = ?`, sessionID).Scan(&got))
	if got != want {
		t.Fatalf("call reservations = %d, want %d", got, want)
	}
}
