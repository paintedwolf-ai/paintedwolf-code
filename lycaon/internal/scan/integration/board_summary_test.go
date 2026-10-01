package integration

import (
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTopLocationsOrderStable(t *testing.T) {
	findings := []api.SecurityFinding{
		scanfindings.FixtureFinding("r1", api.FindingLevelMedium, "m", "b.go", 2),
		scanfindings.FixtureFinding("r2", api.FindingLevelCritical, "c", "a.go", 1),
		scanfindings.FixtureFinding("r3", api.FindingLevelCritical, "c2", "a.go", 1),
	}
	got := scan.TopLocationsFromFindings(findings, 3)
	if len(got) != 2 {
		t.Fatalf("top = %#v", got)
	}
	if got[0].URI != "a.go" || got[0].StartLine != 1 {
		t.Fatalf("critical hotspot first = %#v", got[0])
	}
}

func TestZeroFindingsBoardRollupStillHasHead(t *testing.T) {
	rollup := scan.BuildBoardRollup(&api.CodeScan{
		HeadSHA: "abcdef0123456789",
		Status:  api.CodeScanStatusComplete,
	})
	if rollup.HeadShort != "abcdef01" {
		t.Fatalf("head_short = %q", rollup.HeadShort)
	}
}

func TestBuildBoardCompareSliceTopNew(t *testing.T) {
	slice := scan.BuildBoardCompareSlice("baseline-1", &scan.Comparison{
		NewCount: 1,
		NewFindings: []api.SecurityFinding{
			scanfindings.FixtureFinding("gitleaks:token", api.FindingLevelHigh, "leak", "secrets.env", 2),
		},
	})
	if slice == nil || slice.BaselineAssessmentID != "baseline-1" || slice.ScannersCompared != 1 || slice.NewCount != 1 {
		t.Fatalf("slice = %+v", slice)
	}
	if len(slice.TopNew) != 1 || slice.TopNew[0].File != "secrets.env" {
		t.Fatalf("top_new = %+v", slice.TopNew)
	}
}
