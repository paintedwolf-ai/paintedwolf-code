package workflowadmin

import (
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// A run's deliverable is the report that stated run scope. Phase reports, other
// runs, ungrounded rows, and unstated scope are all excluded.
func TestLastRunCompletionReport_StatedScope(t *testing.T) {
	runID := "run_report_scope"
	runScope := &wire.CompletionReportMeta{Scope: wire.CompletionReportScopeRun, Phase: "report"}
	phaseScope := &wire.CompletionReportMeta{Scope: wire.CompletionReportScopePhase, Phase: "execute"}
	msgs := []wire.Message{
		{ID: "1", Role: wire.MessageRoleUser, Content: "go", WorkflowRunID: runID},
		{ID: "2", Role: wire.MessageRoleAssistant, Content: "ungrounded", WorkflowRunID: runID, Visibility: wire.MessageVisibilityTranscript},
		{ID: "3", Role: wire.MessageRoleAssistant, Kind: wire.MessageKindCompletionReport, CompletionReport: runScope, Content: "first grounded", WorkflowRunID: runID, Visibility: wire.MessageVisibilityTranscript, Grounding: &wire.CitationGrounding{Traced: true}},
		{ID: "4", Role: wire.MessageRoleAssistant, Kind: wire.MessageKindCompletionReport, CompletionReport: runScope, Content: "other run", WorkflowRunID: "other", Visibility: wire.MessageVisibilityTranscript, Grounding: &wire.CitationGrounding{Traced: true}},
		{ID: "5", Role: wire.MessageRoleAssistant, Kind: wire.MessageKindCompletionReport, CompletionReport: runScope, Content: "last grounded", WorkflowRunID: runID, Visibility: wire.MessageVisibilityTranscript, Grounding: &wire.CitationGrounding{CitedURLs: []string{"https://example.com"}}},
		{ID: "6", Role: wire.MessageRoleAssistant, Kind: wire.MessageKindCompletionReport, CompletionReport: phaseScope, Content: "phase report", WorkflowRunID: runID, Visibility: wire.MessageVisibilityTranscript, Grounding: &wire.CitationGrounding{Traced: true}},
		{ID: "7", Role: wire.MessageRoleAssistant, Kind: wire.MessageKindCompletionReport, Content: "scope unstated", WorkflowRunID: runID, Visibility: wire.MessageVisibilityTranscript, Grounding: &wire.CitationGrounding{Traced: true}},
		{ID: "8", Role: wire.MessageRoleAssistant, Kind: wire.MessageKindCompletionReport, CompletionReport: runScope, Content: "hidden", WorkflowRunID: runID, Visibility: wire.MessageVisibilityInternal, Grounding: &wire.CitationGrounding{Traced: true}},
		{ID: "9", Role: wire.MessageRoleAssistant, Kind: wire.MessageKindCompletionReport, CompletionReport: &wire.CompletionReportMeta{Scope: wire.CompletionReportScopeSession}, Content: "ordinary reply", WorkflowRunID: runID, Visibility: wire.MessageVisibilityTranscript, Grounding: &wire.CitationGrounding{Traced: true}},
	}
	got := lastRunCompletionReport(msgs, runID)
	if got == nil || got.Content != "last grounded" {
		t.Fatalf("got = %+v want last grounded", got)
	}
	if lastRunCompletionReport(msgs, "missing") != nil {
		t.Fatal("want nil for unknown run")
	}
}
