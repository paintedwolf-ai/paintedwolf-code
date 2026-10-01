package storageusage

import "testing"

func TestNewReportPreservesLanesAndScopes(t *testing.T) {
	report := NewReport(
		Usage{Lane: LaneArtifacts, Scope: ScopeProject, UsedBytes: 11},
		Usage{Lane: LaneSourceBlobs, Scope: ScopeDevice, UsedBytes: 7},
	)
	if len(report.Lanes) != 2 || report.Lanes[1].Lane != LaneSourceBlobs || report.Lanes[1].Scope != ScopeDevice {
		t.Fatalf("lanes = %+v", report.Lanes)
	}
}
