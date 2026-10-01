package scan

import (
	"testing"
	"time"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAssessmentAuthorityRequiresDeclaredCoverage(t *testing.T) {
	for _, coverage := range []api.ScanCoverageStatus{
		"", api.ScanCoveragePartial, api.ScanCoverageUnavailable,
		api.ScanCoverageBounded, api.ScanCoverageComplete,
	} {
		name := string(coverage)
		if name == "" {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			store := authorityTestStore(t)
			record := authorityScan("scan", "assessment", t.TempDir(), "snapshot", "sast", api.ScanTargetFull, nil)
			record.Status = api.CodeScanStatusComplete
			record.CoverageStatus = coverage
			record.ExecutionManifest.DefinitionFingerprint = ""
			testutil.FailErr(t, "insert completed scan", store.Insert(t.Context(), record, nil, ""))
			wantAuthority := coverage == api.ScanCoverageComplete || coverage == api.ScanCoverageBounded
			if got := EstablishesAuthority(record); got != wantAuthority {
				t.Fatalf("scan authority = %t, want %t", got, wantAuthority)
			}
			var complete bool
			err := store.db.QueryRowContext(t.Context(),
				"SELECT is_complete FROM security_assessments WHERE id = ?", record.AssessmentID).Scan(&complete)
			testutil.FailErr(t, "read persisted authority", err)
			if complete != wantAuthority {
				t.Fatalf("persisted authority = %t, want %t", complete, wantAuthority)
			}
			view, err := store.AssessmentView(t.Context(), []string{record.CanonicalPath})
			testutil.FailErr(t, "read assessment authority", err)
			if (view.CurrentID != "") != wantAuthority {
				t.Fatalf("current assessment = %q, want authority %t", view.CurrentID, wantAuthority)
			}
			summary := BuildAssessmentSummary(record.AssessmentID, []api.CodeScan{record})
			if summary.CoverageStatus != coverage {
				t.Fatalf("summary coverage = %q, want %q", summary.CoverageStatus, coverage)
			}
		})
	}
}

func TestAssessmentSummaryDoesNotFillMissingMemberCoverage(t *testing.T) {
	summary := BuildAssessmentSummary("assessment", []api.CodeScan{
		{ScannerID: "sast", Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoverageComplete},
		{ScannerID: "secrets", Status: api.CodeScanStatusComplete},
	})
	if summary.CoverageStatus != "" {
		t.Fatalf("coverage = %q, want missing member coverage to remain unknown", summary.CoverageStatus)
	}
}

func TestAssessmentRecencyBreaksTimestampTiesByAdmission(t *testing.T) {
	for _, multipleRoots := range []bool{false, true} {
		name := "one root"
		if multipleRoots {
			name = "multiple roots"
		}
		t.Run(name, func(t *testing.T) {
			store := authorityTestStore(t)
			root := t.TempDir()
			paths := []string{root}
			at := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
			ids := []string{"z-oldest", "m-middle", "a-newest"}
			for i, id := range ids {
				if multipleRoots && i > 0 {
					root = t.TempDir()
					paths = append(paths, root)
				}
				record := authorityScan("scan-"+id, id, root, "snapshot-"+id, "sast", api.ScanTargetFull, nil)
				record.CreatedAt = at
				testutil.FailErr(t, "insert assessment "+id, store.Insert(t.Context(), record, nil, ""))
				completeNextScan(t, store, scanfindings.FixtureFinding(id, api.FindingLevelHigh, id, "main.go", 1))
			}
			view, err := store.AssessmentView(t.Context(), paths)
			testutil.FailErr(t, "read tied assessments", err)
			if view.LatestAttemptID != ids[2] || view.CurrentID != ids[2] || view.PreviousID != ids[1] {
				t.Fatalf("assessment order = latest %s, current %s, previous %s", view.LatestAttemptID, view.CurrentID, view.PreviousID)
			}
			if !multipleRoots {
				comparison, err := (&CoordinatorImpl{Store: store}).BoardComparison(t.Context(), view.Previous[0], view.Current[0])
				testutil.FailErr(t, "read forward comparison", err)
				if comparison.NewCount != 1 || comparison.ResolvedCount != 1 {
					t.Fatalf("comparison = %+v, want the newest transition", comparison)
				}
			}
		})
	}
}
