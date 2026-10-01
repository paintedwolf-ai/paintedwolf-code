package execution

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLiveRunnerRescuesItsClaimBeforeExpirySweep(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	store := scanbase.NewSQLStore(sqlDB)
	store.SetEventOutbox(eventoutbox.New(sqlDB, nil))
	testutil.FailErr(t, "insert", store.Insert(t.Context(), api.CodeScan{
		ID: "scan-resume", CanonicalPath: projectDir, Categories: []api.ScanCategory{api.ScanCategorySAST},
		Status: api.CodeScanStatusPending, SourceSnapshotID: "snapshot-1",
	}, nil, ""))
	claimed, err := store.ClaimNext(t.Context())
	testutil.FailErr(t, "claim", err)
	_, err = sqlDB.ExecContext(t.Context(), `UPDATE code_scans SET lease_expires_at = ? WHERE id = ?`, db.FormatTime(time.Now().Add(-time.Minute)), claimed.ID)
	testutil.FailErr(t, "expire", err)

	runner := NewRunner(store, nil, nil, scancfg.RunnerConfig{}, nil)
	_, execution := runner.registerExecution(t.Context(), claimed)
	defer runner.unregisterExecution(claimed.ID, execution)
	runner.renewActiveClaims(t.Context())
	recovered, err := store.RecoverExpiredClaims(t.Context())
	testutil.FailErr(t, "recover after rescue", err)
	if len(recovered) != 0 {
		t.Fatalf("recovered live claim = %+v", recovered)
	}
}
