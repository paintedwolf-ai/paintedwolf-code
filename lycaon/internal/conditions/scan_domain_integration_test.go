//go:build integration

package conditions_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanStubPredicatesWithSQLiteLedger(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "scan_stub.db")

	store := scan.NewSQLStore(sqlDB)
	categories := []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySecret}
	now := time.Now().UTC()
	scanRec := api.CodeScan{
		ID:               "scan-sql-1",
		CanonicalPath:    "/tmp/project",
		Categories:       categories,
		Status:           api.CodeScanStatusComplete,
		DelegationID:     "dep-1",
		HeadSHA:          "head-sql",
		SourceSnapshotID: "head-sql",
		CreatedAt:        now,
		CompletedAt:      &now,
	}
	if err := store.Insert(context.Background(), scanRec, nil, ""); err != nil {
		testutil.FailErr(t, "store.Insert failed", err)
	}
	if err := store.SaveIngest(context.Background(), scanRec.ID, evidence.Record{
		Artifacts: map[string]any{
			"guidance": []api.ScanGuidanceSummary{{Severity: "medium", Message: "ok"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	deps := scanDomainDeps(stubScanLedger{}, nil)
	deps.ScanLedger = store
	deps.SourceSnapshots = staticSourceSnapshot{id: "head-sql"}
	reg, err := conditions.NewDefaultRegistry(deps)
	testutil.FailErr(t, "build conditions registry", err)
	ec := conditions.EvalContext{Ctx: context.Background(), SessionID: "sess-1", ProjectDir: "/tmp/project"}

	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"baseline_scan_complete", true},
		{"security_evidence_fresh", true},
		{"no_critical_findings", true},
	} {
		ok, err := reg.Evaluate(tc.id, ec)
		if err != nil {
			t.Fatalf("%s err=%v", tc.id, err)
		}
		if ok != tc.want {
			t.Fatalf("%s = %v want %v", tc.id, ok, tc.want)
		}
	}
}
