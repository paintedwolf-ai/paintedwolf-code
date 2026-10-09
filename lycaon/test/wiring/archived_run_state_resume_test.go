package wiring

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	_ "modernc.org/sqlite"
)

// TestArchivedRunStateResume_100Database verifies that runs in the frozen
// v1.0.0 release store open at the current baseline and still resolve their definitions.
func TestArchivedRunStateResume_100Database(t *testing.T) {
	testArchivedRunStateResumeForRelease(t, "1.0.0")
}

// TestArchivedRunStateResume_101Database verifies the same for the frozen
// v1.0.1 release store.
func TestArchivedRunStateResume_101Database(t *testing.T) {
	testArchivedRunStateResumeForRelease(t, "1.0.1")
}

func testArchivedRunStateResumeForRelease(t *testing.T, releaseVersion string) {
	root := testutil.CheckoutRoot(t)
	corpusDB := filepath.Join(root, "lycaon", "testdata", "upgrade-corpus", releaseVersion, "store.db")
	_, statErr := os.Stat(corpusDB)
	testutil.FailErr(t, "locate released upgrade fixture", statErr)

	// Copy the frozen database into an isolated temp directory.
	tempDir := t.TempDir()
	targetDB := filepath.Join(tempDir, "store.db")

	srcFile, err := os.Open(corpusDB)
	testutil.FailErr(t, "open corpusDB", err)
	defer srcFile.Close()

	dstFile, err := os.Create(targetDB)
	testutil.FailErr(t, "create targetDB", err)

	_, err = io.Copy(dstFile, srcFile)
	testutil.FailErr(t, "copy store.db", err)
	testutil.FailErr(t, "close targetDB", dstFile.Close())

	ctx := context.Background()
	err = db.UpgradeStaged(ctx, targetDB)
	testutil.FailErr(t, "upgrade staged frozen "+releaseVersion+" store", err)

	// The store lands on the current revision.
	database, err := sql.Open("sqlite", targetDB)
	testutil.FailErr(t, "open upgraded "+releaseVersion+" database", err)
	defer database.Close()

	version, err := db.ReadUserVersion(ctx, database)
	testutil.FailErr(t, "read upgraded user_version", err)
	if version != db.SchemaVersion {
		t.Fatalf("upgraded user_version = %d, want %d", version, db.SchemaVersion)
	}

	// Every preserved run must resolve to a live or sealed definition in the stock
	// catalog, so the upgraded store's runs remain resumable.
	catalog, err := extpacks.ResolveStockCatalog(ctx, nil)
	testutil.FailErr(t, "resolve stock catalog", err)
	manifests, _, err := workflowdef.LoadManifestsFromCatalog(catalog)
	testutil.FailErr(t, "load catalog manifests", err)

	// Initialize SQLStore on top of the upgraded database and resume the
	// preserved runs. Note that workflow_runs records the *workflow* version,
	// which is independent of the application release version.
	sqlStore := workflow.NewSQLStore(database)

	rows, err := database.QueryContext(ctx, "SELECT id, workflow_id, workflow_version, current_phase, status FROM workflow_runs")
	testutil.FailErr(t, "query existing workflow runs", err)
	defer rows.Close()

	type runSummary struct {
		id      string
		wfID    string
		version string
		phase   string
		status  string
	}
	var existingRuns []runSummary
	for rows.Next() {
		var s runSummary
		if err := rows.Scan(&s.id, &s.wfID, &s.version, &s.phase, &s.status); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		existingRuns = append(existingRuns, s)
	}
	testutil.FailErr(t, "rows.Err", rows.Err())

	if len(existingRuns) == 0 {
		t.Fatalf("expected at least one workflow run in frozen %s store.db", releaseVersion)
	}

	for _, s := range existingRuns {
		// Preserved runs have workflow version 1.0.0 (independent of app release version)
		if s.version != "1.0.0" {
			t.Errorf("run %s has workflow version %s, want 1.0.0", s.id, s.version)
		}

		// Read run state through RunStore.
		run, err := sqlStore.Get(ctx, s.id)
		testutil.FailErr(t, "sqlStore.Get("+s.id+")", err)
		if run.WorkflowID != s.wfID {
			t.Errorf("run %s workflowID = %s, want %s", s.id, run.WorkflowID, s.wfID)
		}
		if run.WorkflowVersion != "1.0.0" {
			t.Errorf("run %s workflowVersion = %s, want 1.0.0", s.id, run.WorkflowVersion)
		}
		if run.CurrentPhase != s.phase {
			t.Errorf("run %s currentPhase = %s, want %s", s.id, run.CurrentPhase, s.phase)
		}

		key := workflowdef.ManifestKey(run.WorkflowID, run.WorkflowVersion)
		if _, ok := manifests[key]; !ok {
			t.Errorf("run %s pins %s, which the catalog no longer defines", run.ID, key)
		}
	}
}
