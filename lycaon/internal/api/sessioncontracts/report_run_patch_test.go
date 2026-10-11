package sessioncontracts

import (
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestGetWorkflowRunReport_AfterDraftFinalizationPatch(t *testing.T) {
	h := contractfixture.NewReportTestHarness(t)
	run := h.SeedSecuritySurveyRun(t, "run_report_patch_001")
	const messageID = "msg_live_report"
	testutil.FailErr(t, "append live report draft", h.Store.AppendMessages(t.Context(), run.SessionID, wire.Message{
		ID: messageID, Role: wire.MessageRoleAssistant, WorkflowRunID: run.ID,
		Visibility: wire.MessageVisibilityInternal, DraftStatus: wire.DraftStatusLive,
	}))

	existing, err := h.Store.GetMessage(t.Context(), run.SessionID, messageID)
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
	_, err = h.Store.UpdateMessage(t.Context(), run.SessionID, messageID, committed)
	testutil.FailErr(t, "finalize report draft", err)

	stored, err := h.Store.GetMessage(t.Context(), run.SessionID, messageID)
	testutil.FailErr(t, "reload finalized report", err)
	if stored.CompletionReport == nil || stored.CompletionReport.Scope != wire.CompletionReportScopeRun {
		t.Fatalf("completion report metadata = %+v", stored.CompletionReport)
	}
	h.MarkReportDelivered(t, run)
	contractfixture.AssertPDFOK(t, h.GetReport(t, run.ID))
}
