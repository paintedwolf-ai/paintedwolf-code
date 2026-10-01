package cadence

import (
	"fmt"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPresentFindingsKeepIgnoredRowsAndIsolateSeriesAcrossPages(t *testing.T) {
	root := ledgerRoot(t)
	store := ledgerFixture(t, root, "sast", "execution-1")
	cadence := ledgerCadence(store)
	findings := make([]api.SecurityFinding, scanbase.MaxLedgerPageSize+3)
	for i := range findings {
		findings[i] = scanfindings.FixtureFinding(fmt.Sprintf("rule-%04d", i), api.FindingLevelHigh, "present", "a.go", i+1)
	}
	job := ledgerScan(t, store, "many", root, "sast", api.ScanTargetFull, api.ScanCoverageComplete, findings...)
	testutil.FailErr(t, "record current findings", store.RecordFindingEvents(t.Context(), &job, findings, nil, time.Now().UTC()))
	_, err := cadence.AddIgnore(t.Context(), root, api.FindingIgnoreEntry{Path: "a.go", Reason: "fixture material"})
	testutil.FailErr(t, "ignore findings", err)
	page, err := cadence.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 1})
	testutil.FailErr(t, "apply ignore projection", err)
	if len(page.Entries) != 1 || page.Entries[0].State != api.FindingLedgerIgnored {
		t.Fatal("fixture findings must be ignored")
	}
	for _, scope := range []struct {
		root, scanner string
		count         int
	}{
		{root, "sast", len(findings)}, {root, "other", 0}, {ledgerRoot(t), "sast", 0},
	} {
		open, err := store.OpenFindings(t.Context(), scope.root, scope.scanner)
		testutil.FailErr(t, "read scoped present findings", err)
		if len(open) != scope.count {
			t.Fatalf("presence for %s/%s = %d, want %d", scope.root, scope.scanner, len(open), scope.count)
		}
	}
}
