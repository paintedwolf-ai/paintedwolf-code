package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// A session-scoped completion reply is chat content, never a run's report.
func TestSessionCompletionIsNotARunReport(t *testing.T) {
	h := newReportTestHarness(t)
	for _, workflowID := range []string{"plan", "implement", "security-survey"} {
		t.Run(workflowID, func(t *testing.T) {
			run := h.createCompletedRun(t, "run_"+workflowID, workflowID, "done", time.Now().UTC())
			h.seedSessionReport(t, run.SessionID, "completion_"+workflowID)
			h.markReportDelivered(t, run)
			if rec := h.getReport(t, run.ID); rec.Code != http.StatusNotFound {
				t.Fatalf("ordinary reply exposed as run report: status = %d", rec.Code)
			}
		})
	}
}

func (h *reportTestHarness) seedSessionReport(t *testing.T, sessionID, msgID string) {
	t.Helper()
	testutil.FailErr(t, "append session report", h.store.AppendMessages(t.Context(), sessionID, wire.Message{
		ID: msgID, Role: wire.MessageRoleAssistant, Kind: wire.MessageKindCompletionReport,
		Content: "## What changed\n\nThe bucket refills from a monotonic clock (" + msgID + ").",
		CompletionReport: &wire.CompletionReportMeta{
			Scope: wire.CompletionReportScopeSession, SurfaceID: "implement_synthesis",
		},
		Visibility: wire.MessageVisibilityTranscript,
		Grounding: &wire.CitationGrounding{
			Traced: true,
			EvidenceRecords: []wire.CitationGroundingEvidenceRecord{{
				Handle: "read#1", Kind: "read", Path: "internal/limiter/bucket.go", Line: 42,
				Excerpt: "now := time.Now()", Fidelity: "observed",
			}},
		},
	}))
}
