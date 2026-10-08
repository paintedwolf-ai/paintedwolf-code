package wiring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	_ "modernc.org/sqlite"
)

// TestArchivedRunStateResume_100Database verifies the durable-store contract
// for frozen v1.0.0 application release databases.
func TestArchivedRunStateResume_100Database(t *testing.T) {
	testArchivedRunStateResumeForRelease(t, "1.0.0")
}

// TestArchivedRunStateResume_101Database verifies the durable-store contract
// for frozen v1.0.1 application release databases.
func TestArchivedRunStateResume_101Database(t *testing.T) {
	testArchivedRunStateResumeForRelease(t, "1.0.1")
}

func testArchivedRunStateResumeForRelease(t *testing.T, releaseVersion string) {
	root := testutil.CheckoutRoot(t)
	corpusDB := filepath.Join(root, "lycaon", "testdata", "upgrade-corpus", releaseVersion, "store.db")
	if _, err := os.Stat(corpusDB); err != nil {
		t.Skipf("v%s upgrade-corpus store.db not found at %s: %v", releaseVersion, corpusDB, err)
	}

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

	before := fileSHA256(t, targetDB)
	ctx := context.Background()

	err = db.UpgradeStaged(ctx, targetDB)
	var incompatible *db.StoreIncompatibleError
	if errors.As(err, &incompatible) {
		// No registered route covers the shipped shape yet: the store
		// must be refused into recovery non-destructively, exactly as
		// startup would refuse it.
		if !errors.Is(err, db.ErrStoreIncompatible) {
			t.Fatalf("want store-incompatible refusal, got %v", err)
		}
		if after := fileSHA256(t, targetDB); after != before {
			t.Fatal("refusal modified the frozen store bytes")
		}
		return
	}
	testutil.FailErr(t, "upgrade staged frozen "+releaseVersion+" store", err)

	// The registered route landed the store on the current revision.
	database, err := sql.Open("sqlite", targetDB)
	testutil.FailErr(t, "open upgraded "+releaseVersion+" database", err)
	defer database.Close()

	version, err := db.ReadUserVersion(ctx, database)
	testutil.FailErr(t, "read upgraded user_version", err)
	if version != db.SchemaVersion {
		t.Fatalf("upgraded user_version = %d, want %d", version, db.SchemaVersion)
	}

	// The upgrade created or verified the current provenance table.
	var tableName string
	err = database.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='table' AND name='workflow_run_unit_provenance'",
	).Scan(&tableName)
	testutil.FailErr(t, "verify workflow_run_unit_provenance table existence", err)
	if tableName != "workflow_run_unit_provenance" {
		t.Fatalf("expected workflow_run_unit_provenance table, got %q", tableName)
	}

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

		// Verify provenance recording on resumed runs works seamlessly.
		err = sqlStore.RecordUnitProvenance(ctx, run.ID, run.CurrentPhase, prompts.UnitProvenanceRecord{
			UnitKind:      "prompt",
			UnitID:        "coordinator-test",
			SourceTier:    "archive",
			SourcePath:    "path/to/prompt.md",
			ContentSha256: "sha256-test-hash",
		})
		testutil.FailErr(t, "RecordUnitProvenance on resumed run", err)

		records, err := sqlStore.ListUnitProvenance(ctx, run.ID)
		testutil.FailErr(t, "ListUnitProvenance on resumed run", err)
		if len(records) != 1 {
			t.Fatalf("expected 1 provenance record, got %d", len(records))
		}
		if records[0].UnitID != "coordinator-test" {
			t.Errorf("provenance UnitID = %s, want coordinator-test", records[0].UnitID)
		}
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	testutil.FailErr(t, "open "+path+" for digest", err)
	defer file.Close()
	sum := sha256.New()
	_, err = io.Copy(sum, file)
	testutil.FailErr(t, "digest "+path, err)
	return hex.EncodeToString(sum.Sum(nil))
}
