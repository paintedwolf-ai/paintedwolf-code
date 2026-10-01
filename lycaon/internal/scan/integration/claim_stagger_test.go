package integration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestClaimNextPrefersNonSAST(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claim.db")
	sqlDB := testdbfixture.OpenPath(t, path)

	store := scan.NewSQLStore(sqlDB)
	dir := t.TempDir()
	ctx := context.Background()

	// Non-SAST scans claim before older SAST scans.
	testutil.FailErr(t, "insert sast", store.Insert(ctx, api.CodeScan{
		ID: "sast-1", CanonicalPath: dir, Status: api.CodeScanStatusPending,
		ScannerID: "lycaon-sast", Categories: []api.ScanCategory{api.ScanCategorySAST},
	}, nil, ""))
	testutil.FailErr(t, "insert secrets", store.Insert(ctx, api.CodeScan{
		ID: "sec-1", CanonicalPath: dir, Status: api.CodeScanStatusPending,
		ScannerID: "lycaon-secrets", Categories: []api.ScanCategory{api.ScanCategorySecret},
	}, nil, ""))

	got, err := store.ClaimNext(ctx)
	testutil.FailErr(t, "ClaimNext", err)
	if got.ScannerID != "lycaon-secrets" {
		t.Fatalf("claimed %q want lycaon-secrets (soft stagger)", got.ScannerID)
	}
}
