package scan

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestExpiredScanClaimBecomesExplicitFailure(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	store := NewSQLStore(sqlDB)
	store.SetEventOutbox(eventoutbox.New(sqlDB, nil))
	testutil.FailErr(t, "insert", store.Insert(t.Context(), api.CodeScan{
		ID: "scan-1", CanonicalPath: projectDir, Categories: []api.ScanCategory{api.ScanCategorySAST},
		Status: api.CodeScanStatusPending, SourceSnapshotID: "snapshot-1",
	}, nil, ""))
	claimed, err := store.ClaimNext(t.Context())
	testutil.FailErr(t, "claim", err)
	if claimed.ClaimToken == "" || claimed.Attempt != 1 {
		t.Fatalf("claim = %+v", claimed)
	}
	ok, err := store.RenewClaim(t.Context(), claimed.ID, "wrong-token")
	testutil.FailErr(t, "renew wrong token", err)
	if ok {
		t.Fatal("wrong token renewed claim")
	}
	_, err = sqlDB.ExecContext(t.Context(), `UPDATE code_scans SET lease_expires_at = ? WHERE id = ?`, db.FormatTime(time.Now().Add(-time.Minute)), claimed.ID)
	testutil.FailErr(t, "expire", err)
	recovered, err := store.RecoverExpiredClaims(t.Context())
	testutil.FailErr(t, "recover", err)
	if len(recovered) != 1 || recovered[0].Status != api.CodeScanStatusFailed || recovered[0].ClaimToken != "" {
		t.Fatalf("recovered = %+v", recovered)
	}
	var events int
	testutil.FailErr(t, "count outbox events", sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM event_outbox WHERE topic = 'scan' AND project_id = ?`, testdbseed.DefaultProjectID).Scan(&events))
	if events < 3 {
		t.Fatalf("scan outbox events = %d want at least 3", events)
	}
}

func TestScanCompletionRequiresCurrentClaim(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	store := NewSQLStore(sqlDB)
	testutil.FailErr(t, "insert scan", store.Insert(t.Context(), api.CodeScan{
		ID: "scan-1", CanonicalPath: t.TempDir(), Categories: []api.ScanCategory{api.ScanCategorySAST},
		Status: api.CodeScanStatusPending, SourceSnapshotID: "snapshot-1",
	}, nil, ""))
	if _, err := store.MarkComplete(t.Context(), &api.CodeScan{ID: "scan-1"}, &scanoutput.Result{}); err == nil {
		t.Fatal("empty claim token was accepted")
	}
	claimed, err := store.ClaimNext(t.Context())
	testutil.FailErr(t, "claim scan", err)
	if _, err := store.FinalizeCompleteJSON(t.Context(), claimed, []byte("not json"), nil, nil, api.ScanCoverageComplete); err == nil {
		t.Fatal("invalid result JSON was accepted")
	}
	stale := *claimed
	stale.ClaimToken = "stale"
	won, err := store.MarkComplete(t.Context(), &stale, &scanoutput.Result{})
	testutil.FailErr(t, "stale completion", err)
	if won {
		t.Fatal("stale claim completed scan")
	}
	won, err = store.MarkComplete(t.Context(), claimed, &scanoutput.Result{})
	testutil.FailErr(t, "complete scan", err)
	if !won {
		t.Fatal("current claim did not complete scan")
	}
}
