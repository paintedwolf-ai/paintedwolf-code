package execution

import (
	"fmt"
	"testing"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRunnerConfirmsAbsenceOnlyWithSufficientCoverage(t *testing.T) {
	for _, prior := range []api.ScanCoverageStatus{api.ScanCoveragePartial, api.ScanCoverageBounded, api.ScanCoverageUnavailable} {
		for _, target := range []api.ScanTargetKind{api.ScanTargetFull, api.ScanTargetPaths} {
			for _, coverage := range []api.ScanCoverageStatus{api.ScanCoverageComplete, api.ScanCoverageBounded, api.ScanCoveragePartial, api.ScanCoverageUnavailable} {
				t.Run(fmt.Sprintf("%s/%s/%s", prior, target, coverage), func(t *testing.T) {
					root := ledgerRoot(t)
					store := ledgerFixture(t, root, "sast", "execution-1")
					runner := &Runner{Store: store}
					finding := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a", "a.go", 1)
					first := ledgerScan(t, store, "first", root, "sast", api.ScanTargetFull, api.ScanCoverageComplete, finding)
					first.ExecutionFingerprint = "execution-1"
					runner.recordFindingHistory(t.Context(), &first)
					absent := ledgerScan(t, store, "absent", root, "sast", api.ScanTargetFull, prior)
					absent.ExecutionFingerprint = "execution-1"
					runner.recordFindingHistory(t.Context(), &absent)
					before := ledgerStates(t, store, root)["rule-a"]
					clean := ledgerScan(t, store, "clean", root, "sast", target, coverage)
					clean.ExecutionFingerprint = "execution-1"
					runner.recordFindingHistory(t.Context(), &clean)
					runner.recordFindingHistory(t.Context(), &clean)
					want := before
					if coverage == api.ScanCoverageComplete || (target == api.ScanTargetPaths && coverage == api.ScanCoverageBounded) {
						want = api.FindingLedgerFixed
					}
					if got := ledgerStates(t, store, root)["rule-a"]; got != want {
						t.Fatalf("state = %q, want %q", got, want)
					}
					page, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{})
					testutil.FailErr(t, "read confirmation count", err)
					wantObservations := 2
					if want == api.FindingLedgerFixed {
						wantObservations++
					}
					if page.Entries[0].Observations != wantObservations {
						t.Fatalf("observations = %d, want %d", page.Entries[0].Observations, wantObservations)
					}
				})
			}
		}
	}
}

func TestRunnerConfirmationPreservesScopeAndReopening(t *testing.T) {
	for _, emptyScope := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty_scope_%t", emptyScope), func(t *testing.T) {
			root := ledgerRoot(t)
			store := ledgerFixture(t, root, "sast", "execution-1")
			runner := &Runner{Store: store}
			findings := []api.SecurityFinding{
				scanfindings.FixtureFinding("returns", api.FindingLevelHigh, "returns", "a.go", 1),
				scanfindings.FixtureFinding("absent", api.FindingLevelHigh, "absent", "a.go", 2),
				scanfindings.FixtureFinding("outside", api.FindingLevelHigh, "outside", "b.go", 1),
			}
			first := ledgerScan(t, store, "first", root, "sast", api.ScanTargetFull, api.ScanCoverageComplete, findings...)
			first.ExecutionFingerprint = "execution-1"
			runner.recordFindingHistory(t.Context(), &first)
			absent := ledgerScan(t, store, "absent", root, "sast", api.ScanTargetFull, api.ScanCoveragePartial)
			absent.ExecutionFingerprint = "execution-1"
			runner.recordFindingHistory(t.Context(), &absent)
			clean := ledgerScan(t, store, "clean", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete, findings[0])
			clean.ExecutionFingerprint = "execution-1"
			if emptyScope {
				clean.TargetPaths = nil
			}
			runner.recordFindingHistory(t.Context(), &clean)
			states := ledgerStates(t, store, root)
			wantAbsent := api.FindingLedgerFixed
			if emptyScope {
				wantAbsent = api.FindingLedgerNotObserved
			}
			if states["returns"] != api.FindingLedgerReopened || states["absent"] != wantAbsent || states["outside"] != api.FindingLedgerNotObserved {
				t.Fatalf("states after scoped confirmation = %#v", states)
			}
		})
	}
}

func TestRunnerConfirmationTraversesLedgerPagesAndExecutionChanges(t *testing.T) {
	root := ledgerRoot(t)
	store := ledgerFixture(t, root, "sast", "execution-1")
	runner := &Runner{Store: store}
	findings := make([]api.SecurityFinding, scanbase.MaxLedgerPageSize+1)
	for i := range findings {
		findings[i] = scanfindings.FixtureFinding(fmt.Sprintf("rule-%04d", i), api.FindingLevelHigh, "a", "a.go", i+1)
	}
	for _, step := range []struct {
		id        string
		execution string
		coverage  api.ScanCoverageStatus
		findings  []api.SecurityFinding
	}{
		{"first", "execution-1", api.ScanCoverageComplete, findings},
		{"absent", "execution-1", api.ScanCoveragePartial, nil},
		{"confirmed", "execution-1", api.ScanCoverageComplete, nil},
		{"unchanged", "execution-1", api.ScanCoverageComplete, nil},
		{"new-execution", "execution-2", api.ScanCoverageComplete, nil},
	} {
		job := ledgerScan(t, store, step.id, root, "sast", api.ScanTargetFull, step.coverage, step.findings...)
		job.ExecutionFingerprint = step.execution
		runner.recordFindingHistory(t.Context(), &job)
		runner.recordFindingHistory(t.Context(), &job)
	}
	// Confirmation uses the completing job before the series projection advances.
	testutil.FailErr(t, "advance scanner identity", store.UpsertSeries(t.Context(), scanbase.SeriesRow{
		CanonicalPath: root, ScannerID: "sast", LastCoveredExecutionFingerprint: "execution-2",
	}))
	cursor := ""
	count := 0
	for {
		page, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: scanbase.MaxLedgerPageSize, Cursor: cursor})
		testutil.FailErr(t, "read confirmed page", err)
		if page.TotalMatch != len(findings) || len(page.Entries) == 0 {
			t.Fatalf("missing ledger page: %#v", page)
		}
		for _, entry := range page.Entries {
			if entry.State != api.FindingLedgerFixed || entry.Observations != 4 {
				t.Fatalf("confirmation state/count = %s/%d, want fixed/4", entry.State, entry.Observations)
			}
		}
		count += len(page.Entries)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if count != len(findings) {
		t.Fatalf("read %d entries, want %d", count, len(findings))
	}
}

func TestFindingConfirmationSeparatesScannerAndRoot(t *testing.T) {
	root := ledgerRoot(t)
	otherRoot := ledgerRoot(t)
	store := ledgerFixture(t, root, "sast", "execution-1")
	runner := &Runner{Store: store}
	for i, series := range []struct{ root, scanner string }{
		{root, "sast"}, {root, "other-scanner"}, {otherRoot, "sast"},
	} {
		testutil.FailErr(t, "select scanner series", store.UpsertSeries(t.Context(), scanbase.SeriesRow{
			CanonicalPath: series.root, ScannerID: series.scanner, LastCoveredExecutionFingerprint: "execution-1",
		}))
		finding := scanfindings.FixtureFinding(fmt.Sprintf("rule-%d", i), api.FindingLevelHigh, "a", "a.go", 1)
		first := ledgerScan(t, store, fmt.Sprintf("first-%d", i), series.root, series.scanner, api.ScanTargetFull, api.ScanCoverageComplete, finding)
		first.ExecutionFingerprint = "execution-1"
		runner.recordFindingHistory(t.Context(), &first)
		absent := ledgerScan(t, store, fmt.Sprintf("absent-%d", i), series.root, series.scanner, api.ScanTargetFull, api.ScanCoveragePartial)
		absent.ExecutionFingerprint = "execution-1"
		runner.recordFindingHistory(t.Context(), &absent)
	}
	clean := ledgerScan(t, store, "clean", root, "sast", api.ScanTargetFull, api.ScanCoverageComplete)
	clean.ExecutionFingerprint = ""
	runner.recordFindingHistory(t.Context(), &clean)
	if ledgerStates(t, store, root)["rule-0"] != api.FindingLedgerNotObserved {
		t.Fatal("scan without execution identity confirmed an earlier absence")
	}
	clean.ExecutionFingerprint = "execution-1"
	runner.recordFindingHistory(t.Context(), &clean)
	states := ledgerStates(t, store, root)
	if states["rule-0"] != api.FindingLedgerFixed || states["rule-1"] != api.FindingLedgerNotObserved {
		t.Fatalf("scanner isolation = %#v", states)
	}
	if got := ledgerStates(t, store, otherRoot)["rule-2"]; got != api.FindingLedgerNotObserved {
		t.Fatalf("other root state = %q, want not_observed", got)
	}
}
