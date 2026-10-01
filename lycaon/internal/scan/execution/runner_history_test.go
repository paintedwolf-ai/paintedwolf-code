package execution

import (
	"testing"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRunnerRecordsFirstObservationEvenWhenBaseContainsFinding(t *testing.T) {
	root := ledgerRoot(t)
	store := ledgerFixture(t, root, "sast", "execution-1")
	runner := &Runner{Store: store}
	finding := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "unsafe input", "a.go", 7)
	// Engines may supply a fingerprint that normalization replaces at ingestion.
	finding.Fingerprints.Primary = "engine-local-identity"
	result := &scanoutput.Result{Findings: []api.SecurityFinding{finding}}
	scanoutput.NormalizeResultPaths(result, root)
	job := ledgerScan(t, store, "first-observation", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete, result.Findings...)
	job.Delta = &api.ScanDelta{BaseSnapshotID: "unobserved-base", Status: "complete", Counts: &api.ScanDeltaCounts{Persisted: 1}}
	if len(job.Findings) != 1 || scanbase.FindingIdentity(job.Findings[0]) == scanbase.FindingIdentity(finding) {
		t.Fatalf("fixture did not normalize the engine identity: %#v", job.Findings)
	}
	runner.recordFindingHistory(t.Context(), &job)
	runner.recordFindingHistory(t.Context(), &job)
	page, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 100})
	testutil.FailErr(t, "read ledger", err)
	if len(page.Entries) != 1 || page.Entries[0].Observations != 1 {
		t.Fatalf("first observation and replay = %#v, want one ledger observation", page.Entries)
	}
	if scanbase.FindingIdentity(page.Entries[0].Finding) != scanbase.FindingIdentity(job.Findings[0]) {
		t.Fatal("ledger identity differs from the stored finding")
	}
	stored, err := store.Get(t.Context(), job.ID)
	testutil.FailErr(t, "read scan history", err)
	if len(stored.Findings) != 1 || stored.Findings[0].History == nil || stored.Findings[0].History.IntroducedScanID != job.ID {
		t.Fatalf("stored finding has no matching introduction: %#v", stored.Findings)
	}
}

func TestRunnerHistoryPreservesFindingsOutsideCompletedScope(t *testing.T) {
	for _, target := range []api.ScanTargetKind{api.ScanTargetPaths, api.ScanTargetFull} {
		t.Run(string(target), func(t *testing.T) {
			root := ledgerRoot(t)
			store := ledgerFixture(t, root, "sast", "execution-1")
			runner := &Runner{Store: store}
			a := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a", "a.go", 1)
			b := scanfindings.FixtureFinding("rule-b", api.FindingLevelHigh, "b", "b.go", 1)
			first := ledgerScan(t, store, "first", root, "sast", api.ScanTargetFull, api.ScanCoverageComplete, a, b)
			runner.recordFindingHistory(t.Context(), &first)
			clean := ledgerScan(t, store, "clean", root, "sast", target, api.ScanCoverageComplete)
			runner.recordFindingHistory(t.Context(), &clean)
			open, err := store.OpenFindings(t.Context(), root, "sast")
			testutil.FailErr(t, "read open findings", err)
			wantOpen := 0
			if target == api.ScanTargetPaths {
				wantOpen = 1
			}
			if len(open) != wantOpen {
				t.Fatalf("open findings after clean %s scan = %#v, want %d", target, open, wantOpen)
			}
			for _, finding := range open {
				if scanfindings.PrimaryURI(finding) != "b.go" {
					t.Fatalf("retained finding in scanned path: %#v", finding)
				}
			}
		})
	}
}
