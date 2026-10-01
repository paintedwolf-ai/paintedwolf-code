package packboard_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanPulseSigIncludesRegressionWhenComparePresent(t *testing.T) {
	base := packboard.ScanPulseSig(&api.BoardScansSlice{
		CurrentAssessment: &api.BoardScanSummary{
			AssessmentID:  "scan-new",
			Status:        api.CodeScanStatusComplete,
			FindingsCount: 4,
		},
		Compare: &api.BoardScanCompareSlice{
			NewCount: 1,
			NewByLevel: map[string]int{
				string(api.FindingLevelCritical): 1,
			},
		},
	})
	if !strings.Contains(base, ":regression:1:1") {
		t.Fatalf("sig = %q want regression suffix", base)
	}

	resolved := packboard.ScanPulseSig(&api.BoardScansSlice{
		CurrentAssessment: &api.BoardScanSummary{
			AssessmentID:  "scan-fixed",
			Status:        api.CodeScanStatusComplete,
			FindingsCount: 1,
		},
		Compare: &api.BoardScanCompareSlice{
			ResolvedCount: 2,
		},
	})
	if !strings.Contains(resolved, ":regression:0:0") {
		t.Fatalf("resolved sig = %q", resolved)
	}

	noDelta := packboard.ScanPulseSig(&api.BoardScansSlice{
		CurrentAssessment: &api.BoardScanSummary{
			AssessmentID:  "scan-same",
			Status:        api.CodeScanStatusComplete,
			FindingsCount: 3,
		},
		Compare: &api.BoardScanCompareSlice{},
	})
	if strings.Contains(noDelta, ":regression:") {
		t.Fatalf("unchanged compare should not pulse: %q", noDelta)
	}
}
