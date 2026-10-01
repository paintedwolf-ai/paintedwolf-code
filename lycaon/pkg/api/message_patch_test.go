package api_test

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestMergeMessagePatchPreservesWorkflowRunID(t *testing.T) {
	existing := api.Message{
		ID:            "940fad1a-398e-4fdd-8942-25f824bf4ff0",
		Role:          api.MessageRoleAssistant,
		WorkflowRunID: "0ea26756-a763-4c2a-b933-fa6b64064a20",
	}
	patch := api.Message{
		ID:      existing.ID,
		Role:    api.MessageRoleAssistant,
		Content: "",
		ToolCalls: []api.ToolCall{
			{ID: "functions.find:0", Name: "find"},
		},
	}
	merged := api.MergeMessagePatch(existing, patch)
	if merged.WorkflowRunID != existing.WorkflowRunID {
		t.Fatalf("workflow_run_id = %q want %q", merged.WorkflowRunID, existing.WorkflowRunID)
	}
	if len(merged.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d want 1", len(merged.ToolCalls))
	}
}

func TestMergeMessagePatchPreservesGrounding(t *testing.T) {
	grounding := &api.CitationGrounding{
		Traced: true,
		Checks: []api.CitationGroundingCheck{
			{ID: "path_citations", Label: "Path citations", Status: api.CitationGroundingCheckStatusPassed},
		},
	}
	existing := api.Message{
		ID:      "a1",
		Role:    api.MessageRoleAssistant,
		Content: "the fix landed",
	}
	patch := api.Message{
		ID:         existing.ID,
		Role:       api.MessageRoleAssistant,
		Content:    existing.Content,
		Visibility: api.MessageVisibilityTranscript,
		Grounding:  grounding,
	}
	merged := api.MergeMessagePatch(existing, patch)
	if merged.Grounding != grounding {
		t.Fatalf("grounding = %+v want attached on commit patch", merged.Grounding)
	}
	if merged.Visibility != api.MessageVisibilityTranscript {
		t.Fatalf("visibility = %q want transcript", merged.Visibility)
	}
}

func TestMergeMessagePatchAppliesEvidenceHandles(t *testing.T) {
	existing := api.Message{ID: "tool-1", Role: api.MessageRoleTool, Content: "result"}
	patch := existing
	patch.Content = "[read#1]\nresult"
	patch.EvidenceHandles = []string{"read#1"}

	merged := api.MergeMessagePatch(existing, patch)
	if len(merged.EvidenceHandles) != 1 || merged.EvidenceHandles[0] != "read#1" {
		t.Fatalf("evidence_handles = %v want read#1", merged.EvidenceHandles)
	}
}

func TestMergeMessagePatchAttachesAndPreservesCompletionReport(t *testing.T) {
	existing := api.Message{
		ID:            "report-1",
		Role:          api.MessageRoleAssistant,
		WorkflowRunID: "run-1",
		Visibility:    api.MessageVisibilityInternal,
	}
	meta := &api.CompletionReportMeta{
		Scope:     api.CompletionReportScopeRun,
		SurfaceID: "implement_synthesis",
		Phase:     "report",
	}
	committed := api.MergeMessagePatch(existing, api.Message{
		ID:               existing.ID,
		Role:             api.MessageRoleAssistant,
		Kind:             api.MessageKindCompletionReport,
		Visibility:       api.MessageVisibilityTranscript,
		CompletionReport: meta,
	})
	if committed.CompletionReport != meta {
		t.Fatalf("completion_report = %+v want attached metadata", committed.CompletionReport)
	}

	streamPatch := api.MergeMessagePatch(committed, api.Message{
		ID:      existing.ID,
		Role:    api.MessageRoleAssistant,
		Content: "updated narrative",
	})
	if streamPatch.CompletionReport != meta {
		t.Fatalf("completion_report = %+v want stable metadata preserved", streamPatch.CompletionReport)
	}
}
