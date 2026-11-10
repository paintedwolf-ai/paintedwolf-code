package workercloseout_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

type acceptedReportHistory struct {
	messages []api.Message
	prompts  int
}

func (h *acceptedReportHistory) GetWorkerJobMessages(context.Context, string, string) ([]api.Message, error) {
	return h.messages, nil
}
func (h *acceptedReportHistory) PromptHostTurn(context.Context, string, store.PromptSubmissionOrigin, string) (*promptresult.Result, error) {
	h.prompts++
	return nil, errors.New("unexpected closeout prompt")
}
func (h *acceptedReportHistory) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, errors.New("accepted report was evaluated again")
}

func TestFinalizationUsesAcceptedReportWithoutReevaluation(t *testing.T) {
	for _, status := range []string{"complete", "partial", "blocked"} {
		t.Run(status, func(t *testing.T) {
			accepted := &api.WorkerReportAcceptance{WorkerJobID: "job", Report: workercompletion.ReportWire(workercompletion.WorkerCompletionReport{LegStatus: status, Brief: "Observed result", Verification: &verification.Assessment{Method: verification.Inspection, Reason: "Inspected the assigned source"}, Findings: []workercompletion.WorkerFinding{{Path: "source.go", Line: 4, Excerpt: "exact accepted excerpt", Cite: "[source.go:4](source.go#L4)"}}}), Grounding: &api.CitationGrounding{Traced: true}}
			history := &acceptedReportHistory{messages: []api.Message{{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "complete_leg", Outcome: api.ToolResultOutcomeCompleted, WorkerReport: accepted, ToolArgs: map[string]any{"brief": "different model input"}}}}}
			raw, err := json.Marshal(history.messages)
			testutil.FailErr(t, "persist acceptance", err)
			history.messages = nil
			testutil.FailErr(t, "restore acceptance", json.Unmarshal(raw, &history.messages))
			out, err := workercloseout.FinalizeWorkerSummaryForChild(t.Context(), history, history, "child", "repo-researcher", workercloseout.WorkerSummaryFinalizeOpts{WorkerJobID: "job", Ledger: history})
			testutil.FailErr(t, "finalize accepted report", err)
			if out.Status != workercloseout.StateFromLegStatus(status) || out.Report.Brief != "Observed result" || out.Report.Findings[0].Line != 4 || !reflect.DeepEqual(out.Grounding, accepted.Grounding) || history.prompts != 0 || out.Report.Verification == nil || out.Report.Verification.Method != verification.Inspection || out.Report.Findings[0].Cite != "[source.go:4](source.go#L4)" {
				t.Fatalf("accepted outcome changed: %+v, prompts=%d", out, history.prompts)
			}
		})
	}
}

func TestAcceptedReportRejectsUncommittedAndOtherJobClaims(t *testing.T) {
	receipt := &api.WorkerReportAcceptance{WorkerJobID: "other", Report: &api.WorkerCompletionReport{LegStatus: "complete", Brief: "Other job"}, Grounding: &api.CitationGrounding{}}
	messages := []api.Message{{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "complete_leg", Outcome: api.ToolResultOutcomeCompleted, WorkerReport: receipt}}, {Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "complete_leg", Outcome: api.ToolResultOutcomeCompleted, ToolArgs: map[string]any{"leg_status": "complete", "brief": "Unvalidated"}}}}
	if _, _, ok := workercompletion.AcceptedWorkerReport(messages, "job"); ok {
		t.Fatal("accepted another job or model arguments")
	}
	receipt.WorkerJobID = "job"
	messages[0].ToolResult.Outcome = api.ToolResultOutcomeRejected
	if _, _, ok := workercompletion.AcceptedWorkerReport(messages, "job"); ok {
		t.Fatal("accepted a rejected invocation")
	}
	messages[0].ToolResult.Outcome = api.ToolResultOutcomeCompleted
	messages = append(messages, api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "complete_leg", Outcome: api.ToolResultOutcomeRejected, ToolArgs: map[string]any{"leg_status": "complete", "brief": "Rejected replacement"}}})
	got, _, ok := workercompletion.AcceptedWorkerReport(messages, "job")
	if !ok || got != receipt {
		t.Fatal("rejected replacement erased accepted receipt")
	}
}
