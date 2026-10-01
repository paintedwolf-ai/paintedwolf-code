package execution

import (
	"fmt"
	"testing"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReturningToEarlierSourceGetsAFreshLedgerObservation(t *testing.T) {
	root := ledgerRoot(t)
	store := ledgerFixture(t, root, "sast", "execution-1")
	runner := &Runner{Store: store}
	finding := scanfindings.FixtureFinding("cycle", api.FindingLevelHigh, "cycle", "a.go", 1)
	for index, present := range []bool{true, false, true, false, true, false} {
		findings := []api.SecurityFinding{}
		if present {
			findings = append(findings, finding)
		}
		id := fmt.Sprintf("cycle-%d", index)
		pending := authorityScan(id, "assessment-"+id, root, fmt.Sprintf("snapshot-%t", present), "sast", api.ScanTargetFull, nil)
		testutil.FailErr(t, "insert repeated source scan", store.Insert(t.Context(), pending, []string{"a.go"}, ""))
		job := completeNextScan(t, store, findings...)
		job.CoverageStatus = api.ScanCoverageComplete
		job.ExecutionFingerprint = "execution-1"
		runner.recordFindingHistory(t.Context(), &job)
		open, err := store.OpenFindings(t.Context(), root, "sast")
		testutil.FailErr(t, "read current presence", err)
		if _, found := open[scanbase.FindingIdentity(finding)]; found != present {
			t.Fatalf("step %d presence = %t, want %t", index, found, present)
		}
		want := api.FindingLedgerFixed
		if index == 0 {
			want = api.FindingLedgerOpen
		} else if present {
			want = api.FindingLedgerReopened
		}
		if got := ledgerStates(t, store, root)["cycle"]; got != want {
			t.Fatalf("step %d state = %s, want %s", index, got, want)
		}
	}
}
