package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func testProjectDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	testutil.FailErr(t, "create source directory", os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		testutil.FailErr(t, "write source fixture", os.WriteFile(filepath.Join(dir, "src", name), []byte("package fixture\n"), 0o644))
	}
	canonical, err := scan.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	return canonical
}

func TestCoordinatorDedupSameDelegationHeadCategories(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	ctx := context.Background()
	req := scan.EnqueueRequest{
		ProjectDir:   testProjectDir(t),
		Categories:   []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySecret},
		DelegationID: "dep-1",
		HeadSHA:      "abc123",
		Trigger:      api.ScanTriggerManual,
	}
	first, err := coord.Enqueue(ctx, req)
	testutil.FailErr(t, "coord.Enqueue failed", err)
	second, err := coord.Enqueue(ctx, req)
	testutil.FailErr(t, "coord.Enqueue failed", err)
	if first.ID != second.ID {
		t.Fatalf("dedup failed: %s vs %s", first.ID, second.ID)
	}
}

func TestCoordinatorDedupDistinctCategories(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	ctx := context.Background()
	base := scan.EnqueueRequest{
		ProjectDir:   testProjectDir(t),
		DelegationID: "dep-1",
		HeadSHA:      "abc123",
		Categories:   []api.ScanCategory{api.ScanCategorySecurity},
		Trigger:      api.ScanTriggerManual,
	}
	a, err := coord.Enqueue(ctx, base)
	testutil.FailErr(t, "coord.Enqueue failed", err)
	base.Categories = []api.ScanCategory{api.ScanCategorySCA}
	b, err := coord.Enqueue(ctx, base)
	testutil.FailErr(t, "coord.Enqueue failed", err)
	if a.ID == b.ID {
		t.Fatal("expected distinct scans for different categories")
	}
}

func TestCoordinatorDedupExplicitScannerAcrossCategorySelectors(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	coord := newTestCoordinator(t, scan.NewSQLStore(sqlDB), nil)
	dir := testProjectDir(t)
	first, err := coord.Enqueue(t.Context(), scan.EnqueueRequest{
		ProjectDir: dir, ScannerID: "lycaon-sca", HeadSHA: "abc123",
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
	})
	testutil.FailErr(t, "enqueue broad category", err)
	second, err := coord.Enqueue(t.Context(), scan.EnqueueRequest{
		ProjectDir: dir, ScannerID: "lycaon-sca", HeadSHA: "abc123",
		Categories: []api.ScanCategory{api.ScanCategorySCA},
	})
	testutil.FailErr(t, "enqueue scanner category", err)
	if second.ID != first.ID {
		t.Fatalf("explicit scanner ran twice for the same source: %s vs %s", first.ID, second.ID)
	}
}

func TestCoordinatorDedupPathScannerOnce(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	ctx := context.Background()
	dir := testProjectDir(t)
	req := scan.EnqueueRequest{
		ProjectDir: dir,
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
		ScannerID:  "lycaon-sast",
		HeadSHA:    "sha1",
		Trigger:    api.ScanTriggerManual,
	}
	first, err := coord.Enqueue(ctx, req)
	testutil.FailErr(t, "coord.Enqueue failed", err)
	second, err := coord.Enqueue(ctx, req)
	testutil.FailErr(t, "coord.Enqueue failed", err)
	if first.ID != second.ID {
		t.Fatalf("path scanner dedup failed: %s vs %s", first.ID, second.ID)
	}
}

func TestCoordinatorDoesNotReuseDifferentExecutionIdentity(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	coord := newTestCoordinator(t, scan.NewSQLStore(sqlDB), staticHead{sha: "deadbeef"})
	dir := testProjectDir(t)
	base := scan.EnqueueRequest{
		ProjectDir: dir, Categories: []api.ScanCategory{api.ScanCategorySAST},
		ScannerID: "lycaon-sast",
		ExecutionManifest: &api.ScanExecutionManifest{
			SchemaVersion: "v1", ScannerID: "lycaon-sast", FingerprintScheme: api.ScanFingerprintScheme,
		},
		ExecutionFingerprint: "execution-a",
	}
	first, err := coord.Enqueue(t.Context(), base)
	testutil.FailErr(t, "enqueue first execution", err)
	base.ExecutionFingerprint = "execution-b"
	second, err := coord.Enqueue(t.Context(), base)
	testutil.FailErr(t, "enqueue changed execution", err)
	if second.ID == first.ID {
		t.Fatal("a changed scanner execution identity reused earlier evidence")
	}
}

func TestCoordinatorDedupSameDelegationPathScope(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	ctx := context.Background()
	base := scan.EnqueueRequest{
		ProjectDir:   testProjectDir(t),
		Categories:   []api.ScanCategory{api.ScanCategorySecurity},
		DelegationID: "dep-1",
		HeadSHA:      "abc123",
		Trigger:      api.ScanTriggerManual,
	}

	legA := base
	legA.Paths = []string{"src/a.go"}
	first, err := coord.Enqueue(ctx, legA)
	testutil.FailErr(t, "enqueue leg A", err)
	again, err := coord.Enqueue(ctx, legA)
	testutil.FailErr(t, "re-enqueue leg A", err)
	if first.ID != again.ID {
		t.Fatalf("same path scope must dedup: %s vs %s", first.ID, again.ID)
	}

	// Delegation legs deduplicate by path scope.
	legB := base
	legB.Paths = []string{"src/b.go"}
	otherLeg, err := coord.Enqueue(ctx, legB)
	testutil.FailErr(t, "enqueue leg B", err)
	if otherLeg.ID == first.ID {
		t.Fatal("a different path scope must not reuse another leg's scan")
	}

	// Whole-project and path-scoped scans remain distinct.
	wholeProject, err := coord.Enqueue(ctx, base)
	testutil.FailErr(t, "enqueue whole project", err)
	if wholeProject.ID == first.ID || wholeProject.ID == otherLeg.ID {
		t.Fatal("whole-project scope must not reuse a path-scoped scan")
	}
}

func TestCoordinatorDedupWithoutDelegationIncludesPathScope(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	coord := newTestCoordinator(t, scan.NewSQLStore(sqlDB), staticHead{sha: "deadbeef"})
	base := scan.EnqueueRequest{
		ProjectDir: testProjectDir(t), Categories: []api.ScanCategory{api.ScanCategorySAST},
		ScannerID: "lycaon-sast",
	}
	first := base
	first.Paths = []string{"src/b.go", "src/a.go"}
	firstScan, err := coord.Enqueue(context.Background(), first)
	testutil.FailErr(t, "enqueue first scope", err)

	same := base
	same.Paths = []string{"src/a.go", "src/b.go", "src/a.go"}
	sameScan, err := coord.Enqueue(context.Background(), same)
	testutil.FailErr(t, "enqueue reordered scope", err)
	if sameScan.ID != firstScan.ID {
		t.Fatalf("same path set did not dedup: %s vs %s", sameScan.ID, firstScan.ID)
	}

	different := base
	different.Paths = []string{"src/c.go"}
	differentScan, err := coord.Enqueue(context.Background(), different)
	testutil.FailErr(t, "enqueue distinct scope", err)
	if differentScan.ID == firstScan.ID {
		t.Fatal("different path scopes must not dedup")
	}
}

func TestCoordinatorEnqueueAdoptsUnboundWorkflowRun(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, staticHead{sha: "deadbeef"})
	ctx := context.Background()
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-adopt", "session-adopt", testdbseed.DefaultProjectID)
	dir := testProjectDir(t)
	first, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir: dir,
		Categories: []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
		ScannerID:  "lycaon-sast",
		Trigger:    api.ScanTriggerManual,
	})
	testutil.FailErr(t, "project_open enqueue", err)

	adopted, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir:    dir,
		Categories:    []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
		ScannerID:     "lycaon-sast",
		Trigger:       api.ScanTriggerPhaseEnter,
		WorkflowRunID: "run-adopt",
	})
	testutil.FailErr(t, "phase_enter enqueue", err)
	if adopted.ID != first.ID {
		t.Fatalf("adopted id = %s want %s", adopted.ID, first.ID)
	}
	if adopted.WorkflowRunID != "run-adopt" {
		t.Fatalf("workflow_run_id = %q", adopted.WorkflowRunID)
	}
}

func TestCoordinatorEnqueueSharesInFlightScanAcrossRuns(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	coord := newTestCoordinator(t, scan.NewSQLStore(sqlDB), staticHead{sha: "deadbeef"})
	ctx := context.Background()
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-a", "session-a", testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-b", "session-b", testdbseed.DefaultProjectID)
	dir := testProjectDir(t)
	first, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir:    dir,
		Categories:    []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
		ScannerID:     "lycaon-sast",
		Trigger:       api.ScanTriggerPhaseEnter,
		WorkflowRunID: "run-a",
		AssessmentID:  "assessment-a",
		RequiredScanners: []string{
			"lycaon-sast",
		},
	})
	testutil.FailErr(t, "first run enqueue", err)

	second, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir:    dir,
		Categories:    []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
		ScannerID:     "lycaon-sast",
		Trigger:       api.ScanTriggerPhaseEnter,
		WorkflowRunID: "run-b",
		AssessmentID:  "assessment-b",
		RequiredScanners: []string{
			"lycaon-sast",
		},
	})
	testutil.FailErr(t, "second run enqueue", err)
	if second.ID != first.ID {
		t.Fatalf("shared in-flight scan = %s want %s", second.ID, first.ID)
	}
	for _, runID := range []string{"run-a", "run-b"} {
		bound, listErr := coord.ListByWorkflowRunID(ctx, runID)
		testutil.FailErr(t, "list "+runID, listErr)
		if len(bound) != 1 || bound[0].ID != first.ID {
			t.Fatalf("%s bindings = %#v", runID, bound)
		}
	}
}

func TestCoordinatorEnqueueSharesCompleteScanAcrossRuns(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, staticHead{sha: "deadbeef"})
	ctx := context.Background()
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-a", "session-a", testdbseed.DefaultProjectID)
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-b", "session-b", testdbseed.DefaultProjectID)
	dir := testProjectDir(t)
	first, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir:    dir,
		Categories:    []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
		ScannerID:     "lycaon-sast",
		Trigger:       api.ScanTriggerPhaseEnter,
		WorkflowRunID: "run-a",
		AssessmentID:  "assessment-a",
		RequiredScanners: []string{
			"lycaon-sast",
		},
	})
	testutil.FailErr(t, "first run enqueue", err)
	claimed := claimScan(t, store, first.ID)
	_, err = store.MarkComplete(ctx, claimed, &scanoutput.Result{})
	testutil.FailErr(t, "MarkComplete", err)

	reused, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir:    dir,
		Categories:    []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
		ScannerID:     "lycaon-sast",
		Trigger:       api.ScanTriggerPhaseEnter,
		WorkflowRunID: "run-b",
		AssessmentID:  "assessment-b",
		RequiredScanners: []string{
			"lycaon-sast",
		},
	})
	testutil.FailErr(t, "second run enqueue", err)
	if reused.ID != first.ID {
		t.Fatalf("complete scan = %s want reuse of %s", reused.ID, first.ID)
	}
	if reused.WorkflowRunID != "run-b" {
		t.Fatalf("reused workflow_run_id = %q", reused.WorkflowRunID)
	}
	if reused.AssessmentID != "assessment-b" {
		t.Fatalf("reused assessment_id = %q", reused.AssessmentID)
	}
	view, err := store.AssessmentView(ctx, []string{dir})
	testutil.FailErr(t, "assessment view", err)
	if view.CurrentID != "assessment-b" || view.PreviousID != "assessment-a" {
		t.Fatalf("assessment view ids = current %q previous %q", view.CurrentID, view.PreviousID)
	}
	if len(view.Current) != 1 || view.Current[0].ID != first.ID || view.Current[0].AssessmentID != "assessment-b" {
		t.Fatalf("current assessment scans = %#v", view.Current)
	}
}

func TestCoordinatorEnqueueDoesNotAdoptFailedUnboundScan(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, staticHead{sha: "deadbeef"})
	ctx := context.Background()
	testdbseed.InsertWorkflowRun(t, sqlDB, "run-retry", "session-retry", testdbseed.DefaultProjectID)
	dir := testProjectDir(t)
	failed, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir: dir,
		Categories: []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
		ScannerID:  "lycaon-sast",
		Trigger:    api.ScanTriggerManual,
	})
	testutil.FailErr(t, "project_open enqueue", err)
	_, err = store.FinalizePendingFailure(ctx, failed.ID, "SCAN_ENGINE_FAILED", "engine crashed")
	testutil.FailErr(t, "MarkFailed", err)

	fresh, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir:    dir,
		Categories:    []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity},
		ScannerID:     "lycaon-sast",
		Trigger:       api.ScanTriggerPhaseEnter,
		WorkflowRunID: "run-retry",
	})
	testutil.FailErr(t, "phase_enter enqueue", err)
	if fresh.ID == failed.ID {
		t.Fatal("failed unbound work must not be adopted")
	}
	if fresh.WorkflowRunID != "run-retry" {
		t.Fatalf("fresh workflow_run_id = %q", fresh.WorkflowRunID)
	}
}

func TestCoordinatorLatestForDelegation(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	ctx := context.Background()
	dir := testProjectDir(t)
	categories := []api.ScanCategory{api.ScanCategorySecurity}
	if _, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir: dir, Categories: categories, DelegationID: "dep-1", HeadSHA: "old",
	}); err != nil {
		testutil.FailErr(t, "enqueue old scan", err)
	}
	latest, err := coord.Enqueue(ctx, scan.EnqueueRequest{
		ProjectDir: dir, Categories: categories, DelegationID: "dep-1", HeadSHA: "new",
	})
	testutil.FailErr(t, "coord.Enqueue failed", err)
	got, err := coord.LatestForDelegation(ctx, "dep-1", categories)
	testutil.FailErr(t, "coord.LatestForDelegation failed", err)
	if got.ID != latest.ID {
		t.Fatalf("latest = %s want %s", got.ID, latest.ID)
	}
}
