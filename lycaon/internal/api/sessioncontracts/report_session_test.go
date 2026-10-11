package sessioncontracts

import (
	"net/http"
	"testing"
	"time"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
)

func TestSessionCompletionIsNotARunReport(t *testing.T) {
	h := contractfixture.NewReportTestHarness(t)
	for _, workflowID := range []string{"plan", "implement", "security-survey"} {
		t.Run(workflowID, func(t *testing.T) {
			run := h.CreateCompletedRun(t, "run_"+workflowID, workflowID, "done", time.Now().UTC())
			h.SeedSessionReport(t, run.SessionID, "completion_"+workflowID)
			h.MarkReportDelivered(t, run)
			if rec := h.GetReport(t, run.ID); rec.Code != http.StatusNotFound {
				t.Fatalf("ordinary reply exposed as run report: status = %d", rec.Code)
			}
		})
	}
}
