package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSQLStoreGetMissingReturnsNilNil(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "scan-get.db")

	store := scan.NewSQLStore(sqlDB)
	rec, err := store.Get(context.Background(), "missing-id")
	testutil.FailErr(t, "Get missing", err)
	if rec != nil {
		t.Fatalf("Get missing = %+v, want nil", rec)
	}
}

func TestCoordinatorQueryMissingScanStructuredReject(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "scan-query.db")

	coord := newTestCoordinator(t, scan.NewSQLStore(sqlDB), nil)
	_, err := coord.Query(context.Background(), scan.QueryRequest{ScanID: "15c17f2c"})
	var reject *scan.DrilldownReject
	if !errors.As(err, &reject) || reject.Code != scan.DrilldownRejectNotFound {
		t.Fatalf("Query missing = %v want DrilldownReject %s", err, scan.DrilldownRejectNotFound)
	}
}
