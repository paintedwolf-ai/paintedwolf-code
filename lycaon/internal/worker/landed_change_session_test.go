package worker

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/scan/obligation"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestInsertLandedChangeAccumulatesSessionPathDeltas(t *testing.T) {
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "landed-session.db")
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, sqlDB, "sess-landed", testdbseed.DefaultProjectID)

	root := t.TempDir()
	now := timelayout.Format(time.Now().UTC())
	for _, jobID := range []string{"job-land-1", "job-land-2"} {
		_, err := sqlDB.ExecContext(ctx, `INSERT INTO worker_jobs (
			id, project_id, parent_session_id, agent_type, status, prompt, brief, created_at
		) VALUES (?, ?, 'sess-landed', 'implementer', 'complete', 'p', 'b', ?)`,
			jobID, testdbseed.DefaultProjectID, now)
		testutil.FailErr(t, "insert worker job", err)
	}

	manifest := api.ScanExecutionManifest{DefinitionFingerprint: "def-fp-1"}
	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin tx", err)
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)
	testutil.FailErr(t, "insert first session landed change", insertLandedChangeTx(ctx, q, obligation.Plan{
		ID:                   "lc-1",
		WorkerJobID:          "job-land-1",
		CanonicalPath:        root,
		SessionID:            "sess-landed",
		ChangedPaths:         []string{"a.go", "b.go"},
		Required:             true,
		PathScoped:           true,
		ScanID:               "scan-land-1",
		AssessmentID:         "assess-land-1",
		ScannerID:            "lycaon-sast",
		ExecutionManifest:    manifest,
		ExecutionFingerprint: "exec-fp-1",
		FingerprintScheme:    "v1",
	}))
	testutil.FailErr(t, "insert second session landed change", insertLandedChangeTx(ctx, q, obligation.Plan{
		ID:                   "lc-2",
		WorkerJobID:          "job-land-2",
		CanonicalPath:        root,
		SessionID:            "sess-landed",
		ChangedPaths:         []string{"c.go"},
		DeletedPaths:         []string{"b.go"},
		Required:             true,
		PathScoped:           true,
		ScanID:               "scan-land-2",
		AssessmentID:         "assess-land-2",
		ScannerID:            "lycaon-sast",
		ExecutionManifest:    manifest,
		ExecutionFingerprint: "exec-fp-2",
		FingerprintScheme:    "v1",
	}))
	testutil.FailErr(t, "commit tx", tx.Commit())

	var bound int
	testutil.FailErr(t, "count session scan bindings", sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_scan_bindings WHERE session_id = 'sess-landed'`).Scan(&bound))
	if bound != 2 {
		t.Fatalf("session scan bindings = %d, want 2", bound)
	}
}
