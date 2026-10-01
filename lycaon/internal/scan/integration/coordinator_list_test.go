package integration

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoordinatorListByCanonicalPath(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "scan.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	projectDir := t.TempDir()

	for i := 0; i < 3; i++ {
		rec := api.CodeScan{
			ID:            "",
			CanonicalPath: projectDir,
			Categories:    []api.ScanCategory{api.ScanCategorySecurity},
			Status:        api.CodeScanStatusComplete,
			CreatedAt:     time.Now().UTC().Add(time.Duration(i) * time.Second),
		}
		if err := store.Insert(context.Background(), rec, nil, ""); err != nil {
			testutil.FailErr(t, "store.Insert failed", err)
		}
	}

	list, err := coord.List(context.Background(), []string{projectDir}, 2)
	testutil.FailErr(t, "coord.List failed", err)
	if len(list) != 2 {
		t.Fatalf("len = %d want 2", len(list))
	}
	if list[0].Findings != nil {
		t.Fatal("summary view should omit findings audit rows")
	}
}
