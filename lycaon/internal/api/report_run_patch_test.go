package api

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// A coordinator closeout reuses its live draft row. The final patch must carry
// the report identity through persistence so the PDF route can resolve it.
func TestGetWorkflowRunReport_AfterDraftFinalizationPatch(t *testing.T) {
	h := newReportTestHarness(t)
	run := h.seedSecuritySurveyRun(t, "run_report_patch_001")
	const messageID = "msg_live_report"
	testutil.FailErr(t, "append live report draft", h.store.AppendMessages(t.Context(), run.SessionID, wire.Message{
		ID: messageID, Role: wire.MessageRoleAssistant, WorkflowRunID: run.ID,
		Visibility: wire.MessageVisibilityInternal, DraftStatus: wire.DraftStatusLive,
	}))

	existing, err := h.store.GetMessage(t.Context(), run.SessionID, messageID)
	testutil.FailErr(t, "load live report draft", err)
	committed := wire.MergeMessagePatch(existing, wire.Message{
		ID: messageID, Role: wire.MessageRoleAssistant,
		Kind: wire.MessageKindCompletionReport, Content: "## Summary\n\nThe survey completed.",
		Visibility: wire.MessageVisibilityTranscript, DraftStatus: wire.DraftStatusCommitted,
		CompletionReport: &wire.CompletionReportMeta{
			Scope: wire.CompletionReportScopeRun, SurfaceID: "implement_synthesis", Phase: "report",
		},
		Grounding: &wire.CitationGrounding{
			Traced: true,
			EvidenceRecords: []wire.CitationGroundingEvidenceRecord{{
				Handle: "read#1", Kind: "read", Path: "internal/auth/session.go", Line: 1,
				Excerpt: "package auth", Fidelity: "observed",
			}},
		},
	})
	_, err = h.store.UpdateMessage(t.Context(), run.SessionID, messageID, committed)
	testutil.FailErr(t, "finalize report draft", err)

	stored, err := h.store.GetMessage(t.Context(), run.SessionID, messageID)
	testutil.FailErr(t, "reload finalized report", err)
	if stored.CompletionReport == nil || stored.CompletionReport.Scope != wire.CompletionReportScopeRun {
		t.Fatalf("completion report metadata = %+v", stored.CompletionReport)
	}
	h.markReportDelivered(t, run)
	assertPDFOK(t, h.getReport(t, run.ID))
}
