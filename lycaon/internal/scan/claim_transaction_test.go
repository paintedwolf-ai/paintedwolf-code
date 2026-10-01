package scan

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type blindReadHandle struct {
	db.Handle
}

func (h blindReadHandle) QueryRowContext(ctx context.Context, _ string, _ ...any) *sql.Row {
	return h.Handle.QueryRowContext(ctx, "SELECT 1 WHERE 0")
}

func TestClaimNextDoesNotDependOnReadPoolVisibility(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	store := NewSQLStore(blindReadHandle{Handle: database})
	testutil.FailErr(t, "insert scan", store.Insert(t.Context(), api.CodeScan{
		ID:                   "scan-1",
		CanonicalPath:        t.TempDir(),
		Categories:           []api.ScanCategory{api.ScanCategorySAST},
		Status:               api.CodeScanStatusPending,
		SourceSnapshotID:     "snapshot-1",
		SourceCaptureQuality: "exact", SourceAdmissionMode: "scope",
	}, nil, ""))

	claimed, err := store.ClaimNext(t.Context())
	testutil.FailErr(t, "claim scan", err)
	if claimed == nil || claimed.ID != "scan-1" || claimed.ClaimToken == "" {
		t.Fatalf("claimed scan = %+v", claimed)
	}
	if claimed.AssessmentID == "" || claimed.ExecutionFingerprint == "" ||
		claimed.SourceCaptureQuality != "exact" || claimed.SourceAdmissionMode != "scope" {
		t.Fatalf("claimed scan lost authority facts: %+v", claimed)
	}
}
