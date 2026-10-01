package opengrep

import "testing"

func TestReportExitRequiresEvidenceForRecoverableOutcomes(t *testing.T) {
	for _, tc := range []struct {
		code, findings, diagnostics int
		valid                       bool
	}{
		{0, 0, 0, true}, {0, 1, 1, true},
		{1, 1, 0, true}, {1, 0, 0, false},
		{2, 1, 1, true}, {2, 1, 0, false},
		{3, 0, 1, true}, {3, 0, 0, false},
		{4, 0, 1, true}, {4, 1, 0, false},
		{5, 0, 1, true}, {5, 0, 0, false},
		{7, 1, 1, false}, {8, 1, 1, false}, {-1, 1, 1, false},
	} {
		if err := ValidateReportExit(tc.code, tc.findings, tc.diagnostics); (err == nil) != tc.valid {
			t.Errorf("exit=%d findings=%d diagnostics=%d: %v", tc.code, tc.findings, tc.diagnostics, err)
		}
	}
}
