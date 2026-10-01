package scan

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type obligationFindingHistory struct {
	introduced int
	calls      int
	since      time.Time
	level      api.FindingLevel
}

func (h *obligationFindingHistory) IntroducedSince(_ context.Context, _ string, since time.Time, level api.FindingLevel) (int, error) {
	h.calls++
	h.since, h.level = since, level
	return h.introduced, nil
}

func TestWorkflowNoNewGateWaitsForFullCoverage(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      api.CodeScanStatus
		coverage    api.ScanCoverageStatus
		introduced  int
		incremental bool
		want        string
		wantReads   int
	}{
		{name: "no rows", want: api.ObligationStatusEmpty},
		{name: "running", status: api.CodeScanStatusRunning, want: api.ObligationStatusPending},
		{name: "failed", status: api.CodeScanStatusFailed, want: api.ObligationStatusFailed},
		{name: "partial", status: api.CodeScanStatusComplete, coverage: api.ScanCoveragePartial, want: api.ObligationStatusFailed},
		{name: "new high findings", status: api.CodeScanStatusComplete, coverage: api.ScanCoverageComplete, introduced: 2, want: api.ObligationStatusFailed, wantReads: 1},
		{name: "no new high findings", status: api.CodeScanStatusComplete, coverage: api.ScanCoverageComplete, want: api.ObligationStatusComplete, wantReads: 1},
		{name: "incremental with bound scans", incremental: true, status: api.CodeScanStatusRunning, introduced: 2, want: api.ObligationStatusFailed, wantReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var scans []api.CodeScan
			if tc.status != "" {
				scans = []api.CodeScan{{ID: "scan-1", Status: tc.status, CoverageStatus: tc.coverage}}
			}
			started := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
			history := &obligationFindingHistory{introduced: tc.introduced}
			projectDir := t.TempDir()
			obligation := &WorkflowObligation{
				Ledger: stubWorkflowScanLedger{scans: scans}, History: history,
				Params: func(context.Context, string, string, string) (map[string]any, error) {
					return map[string]any{"categories": []any{"security"}, "full": !tc.incremental, "gate": "no_new"}, nil
				},
				Runs: func(context.Context, string) (*api.WorkflowRun, error) {
					return &api.WorkflowRun{ProjectID: "project-1", CreatedAt: started}, nil
				},
				Projects: func(context.Context, string) (string, error) { return projectDir, nil },
			}
			got, err := obligation.Status(t.Context(), "run-1", "ingest")
			testutil.FailErr(t, "read no-new status", err)
			if got.Status != tc.want || history.calls != tc.wantReads {
				t.Fatalf("status = %#v, history calls = %d, want %s/%d", got, history.calls, tc.want, tc.wantReads)
			}
			if tc.wantReads == 0 {
				return
			}
			if history.level != api.FindingLevelHigh || !history.since.Equal(started) {
				t.Fatalf("history query = %+v", history)
			}
			if got.Detail["introduced_since_run"] != tc.introduced || (!tc.incremental && got.Detail["scans_terminal"] != 1) {
				t.Fatalf("gate detail lost finding or coverage evidence: %#v", got.Detail)
			}
		})
	}
}
