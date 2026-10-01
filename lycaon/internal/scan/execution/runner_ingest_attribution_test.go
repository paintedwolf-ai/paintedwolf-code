package execution

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/evidence"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type attributionIngester struct {
	meta   scanbase.IngestMeta
	result *scanoutput.Result
}

func (i *attributionIngester) Ingest(_ context.Context, _ scanbase.ScanSource, result *scanoutput.Result, meta scanbase.IngestMeta) (evidence.Record, error) {
	i.meta = meta
	i.result = result
	return evidence.Record{}, nil
}

func TestRunnerIngestUsesExactLandingAttributionAndStablePaths(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	projectDir := t.TempDir()
	now := db.FormatTime(time.Now().UTC())
	_, err := sqlDB.ExecContext(t.Context(), `
		INSERT INTO worker_jobs(id, project_id, workspace_path, agent_type, status, prompt, brief, created_at, merge_status)
		VALUES ('job-1', ?, ?, 'implementer', 'complete', 'fixture', 'fixture', ?, 'merged')
	`, testdbseed.DefaultProjectID, projectDir, now)
	testutil.FailErr(t, "insert worker", err)
	store := scanbase.NewSQLStore(sqlDB)
	snapshots := testSnapshots(t, store)
	rec := api.CodeScan{
		ID: "scan-1", CanonicalPath: projectDir, Categories: []api.ScanCategory{api.ScanCategorySAST},
		ScannerID: "sast-one", Status: api.CodeScanStatusRunning, CreatedAt: time.Now().UTC(),
		DelegationID: "dep-1", SourceSnapshotID: publishTestSnapshot(t, snapshots, projectDir), Trigger: api.ScanTriggerLandedChange,
		ClaimToken: "claim-scan-1",
	}
	testutil.FailErr(t, "insert scan", store.Insert(t.Context(), rec, []string{filepath.Join(projectDir, "src")}, ""))
	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO landed_changes(
			id, worker_job_id, canonical_path, delegation_id, changed_paths_json,
			deleted_paths_json, scan_required, scan_id, created_at
		) VALUES ('landing-1', 'job-1', ?, 'dep-1', '["src/touched.go","src/deleted.go"]',
		          '["src/deleted.go"]', 1, 'scan-1', ?)
	`, projectDir, now)
	testutil.FailErr(t, "insert landing", err)

	// A scanner reports paths under the tree it was handed.
	scanner := &scanningRootScanner{id: "sast-one", categories: []api.ScanCategory{api.ScanCategorySAST}}
	ingester := &attributionIngester{}
	runner := NewRunner(store, &scanbase.MockRegistry{Scanner: scanner}, ingester, scancfg.DefaultRunnerConfig(), nil)
	runner.Snapshots = snapshots
	runner.execute(t.Context(), &rec)

	if len(ingester.meta.TouchedPaths) != 2 || ingester.meta.TouchedPaths[0] != "src/touched.go" || ingester.meta.TouchedPaths[1] != "src/deleted.go" {
		t.Fatalf("ingest touched paths = %#v", ingester.meta.TouchedPaths)
	}
	if ingester.result == nil || ingester.result.Findings[0].Locations[0].URI != "src/touched.go" {
		t.Fatalf("ingest result paths = %#v", ingester.result)
	}
}

// Findings use paths relative to the supplied scan root.
type scanningRootScanner struct {
	id         string
	categories []api.ScanCategory
}

func (s *scanningRootScanner) ID() string                     { return s.id }
func (s *scanningRootScanner) Categories() []api.ScanCategory { return s.categories }

func (s *scanningRootScanner) Run(_ context.Context, req scanbase.ScanRequest) (*scanoutput.Result, error) {
	return &scanoutput.Result{Findings: []api.SecurityFinding{
		scanfindings.FixtureFinding("rule-1", api.FindingLevelHigh, "finding",
			filepath.Join(req.ProjectDir, "src", "touched.go"), 3),
	}}, nil
}
