package sessioncontracts

import (
	"context"
	"github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/report"
	"github.com/lycaon/lycaon/internal/report/reporttest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
	"time"
)

func TestBlockedReviewServesItsRetainedSnapshot(t *testing.T) {
	h := contractfixture.NewReportTestHarness(t)
	run := h.SeedSecuritySurveyRun(t, "run_review_blocked")
	repair := workflow.ReviewRepair{
		ID: "repair", Phase: "claims", State: "blocked", UpdatedAt: time.Now().UTC(),
		Responses: []workflow.ReviewRepairResponse{{ID: "response"}},
		Snapshot:  &workflow.ReviewSnapshot{Vars: map[string]any{}, Unavailable: []string{"scan ledger"}},
	}
	_, err := h.WfMgr.Vars.Stamp(t.Context(), run.ID, func(_ context.Context, _ *wire.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		vars["review_repairs"] = []workflow.ReviewRepair{repair}
		return vars, true, nil
	})
	testutil.FailErr(t, "stamp blocked repair", err)
	pause := func(reason string) {
		t.Helper()
		current, err := h.RunStore.Runs.Get(t.Context(), run.ID)
		testutil.FailErr(t, "get run", err)
		current.Status, current.PauseReason, current.CurrentPhase, current.CompletedAt = wire.WorkflowRunStatusPaused, reason, "claims", nil
		testutil.FailErr(t, "pause run", h.RunStore.State.Update(t.Context(), current))
	}

	pause(workflow.ReviewBlockedReason)
	input, ok, err := h.Srv.Admin.Workflow.Reports.BuildRunReportInput(t.Context(), run.ID)
	testutil.FailErr(t, "build blocked report", err)
	if !ok || input.Kind != report.BlockedReviewSnapshot || input.Completeness() != report.CompletenessIncomplete {
		t.Fatalf("blocked review report = ok %v kind %q", ok, input.Kind)
	}
	if !strings.Contains(input.Synthesis, "Unavailable when paused: scan ledger.") {
		t.Fatalf("snapshot omitted its unavailable sources: %s", input.Synthesis)
	}
	reporttest.AssertPDFOK(t, h.GetReport(t, run.ID))

	pause("user")
	if _, ok, err := h.Srv.Admin.Workflow.Reports.BuildRunReportInput(t.Context(), run.ID); err != nil || ok {
		t.Fatalf("an ordinarily paused run offered a report: ok %v err %v", ok, err)
	}
}
