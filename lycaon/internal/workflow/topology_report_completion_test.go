package workflow

import (
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReportPhaseCompletionNeedsGroundedEvidence(t *testing.T) {
	run := &api.WorkflowRun{ID: "run-1", CurrentPhase: "synthesis"}
	message := func(grounding *api.CitationGrounding) api.Message {
		return api.Message{
			Role: api.MessageRoleAssistant, Kind: api.MessageKindCompletionReport, WorkflowRunID: run.ID, Grounding: grounding,
			CompletionReport: &api.CompletionReportMeta{Phase: run.CurrentPhase, Scope: api.CompletionReportScopePhase},
		}
	}
	for _, tc := range []struct {
		name      string
		grounding *api.CitationGrounding
		want      bool
	}{
		{"cited evidence", &api.CitationGrounding{Traced: true}, true},
		{"host assembled", &api.CitationGrounding{HostAssembled: true}, true},
		{"nothing to cite", &api.CitationGrounding{Traced: true, EvidenceOptional: true}, false},
		{"no grounding", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReportPhaseCompletion(message(tc.grounding), run, workflowdef.Manifest{}); got != tc.want {
				t.Fatalf("ReportPhaseCompletion = %v, want %v", got, tc.want)
			}
		})
	}
}
