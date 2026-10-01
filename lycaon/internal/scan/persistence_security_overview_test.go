package scan

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestFullPassCoverageAccountsForEveryScanner(t *testing.T) {
	complete := api.CodeScan{Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoverageComplete}
	for _, tc := range []struct {
		name  string
		other api.CodeScan
		want  api.ScanCoverageStatus
	}{
		{"complete", complete, api.ScanCoverageComplete},
		{"bounded", api.CodeScan{Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoverageBounded}, api.ScanCoverageBounded},
		{"partial", api.CodeScan{Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoveragePartial}, api.ScanCoveragePartial},
		{"unavailable", api.CodeScan{Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoverageUnavailable}, api.ScanCoveragePartial},
		{"failed", api.CodeScan{Status: api.CodeScanStatusFailed}, api.ScanCoveragePartial},
		{"timed_out", api.CodeScan{Status: api.CodeScanStatusTimedOut}, api.ScanCoveragePartial},
		{"canceled", api.CodeScan{Status: api.CodeScanStatusCanceled}, api.ScanCoveragePartial},
		{"superseded", api.CodeScan{Status: api.CodeScanStatusSuperseded}, api.ScanCoveragePartial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := FullPassMember{Phase: api.FullPassMemberStarted, Scan: &complete}
			other := FullPassMember{Phase: api.FullPassMemberStarted, Scan: &tc.other}
			for _, members := range [][]FullPassMember{{started, other}, {other, started}} {
				if got := passCoverage(members); got != tc.want {
					t.Fatalf("coverage = %s, want %s", got, tc.want)
				}
			}
		})
	}
	failed := api.CodeScan{Status: api.CodeScanStatusFailed}
	if got := passCoverage([]FullPassMember{{Phase: api.FullPassMemberStarted, Scan: &failed}}); got != api.ScanCoverageUnavailable {
		t.Fatalf("failed pass coverage = %s", got)
	}
	notStarted := []FullPassMember{{Phase: api.FullPassMemberStarted, Scan: &complete}, {Phase: api.FullPassMemberNotStarted}}
	if got := passCoverage(notStarted); got != api.ScanCoveragePartial {
		t.Fatalf("coverage with a member that never started = %s, want partial", got)
	}
}
