package execution

import (
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// ledgerRoot returns the canonical root path, which is what scans record and reads look up.
func ledgerRoot(t *testing.T) string {
	t.Helper()
	root, err := scanbase.CanonicalPath(t.TempDir())
	testutil.FailErr(t, "canonical root", err)
	return root
}

// ledgerFixture sets up a project with one scanner series for the ledger join.
func ledgerFixture(t *testing.T, root, scanner, execution string) *scanbase.SQLStore {
	t.Helper()
	store := authorityTestStore(t)
	testutil.FailErr(t, "upsert series", store.UpsertSeries(t.Context(), scanbase.SeriesRow{
		CanonicalPath:                   root,
		ScannerID:                       scanner,
		Categories:                      []api.ScanCategory{api.ScanCategorySAST},
		LastCompletedAt:                 time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
		LastCoveredExecutionFingerprint: execution,
		UpdatedAt:                       time.Now().UTC(),
	}))
	return store
}

func ledgerScan(t *testing.T, store *scanbase.SQLStore, id, root, scanner string, target api.ScanTargetKind, coverage api.ScanCoverageStatus, findings ...api.SecurityFinding) api.CodeScan {
	t.Helper()
	job := authorityScan(id, "assessment-"+id, root, "snapshot-"+id, scanner, target, nil)
	testutil.FailErr(t, "insert scan", store.Insert(t.Context(), job, []string{"a.go"}, ""))
	completed := completeNextScan(t, store, findings...)
	completed.CoverageStatus = coverage
	return completed
}

func ledgerStates(t *testing.T, store *scanbase.SQLStore, root string) map[string]api.FindingLedgerState {
	t.Helper()
	page, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 100})
	testutil.FailErr(t, "read ledger", err)
	out := make(map[string]api.FindingLedgerState, len(page.Entries))
	for _, entry := range page.Entries {
		out[entry.Finding.RuleID] = entry.State
	}
	return out
}
