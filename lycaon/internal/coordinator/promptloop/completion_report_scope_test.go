package promptloop

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDocumentFieldsRequireRunReportBinding(t *testing.T) {
	report := guidance.CoordinatorCompletionReport{
		Headline: "Conclusion", Summary: "Assessment", Limits: []string{"Unchecked"},
		Findings: []guidance.CoordinatorFinding{{Title: "Finding"}},
	}
	for _, binding := range []CompletionReportBinding{{}, {RunID: "plan", Phase: "research"}} {
		meta := completionReportMeta("implement_synthesis", binding, report)
		if meta.Scope == api.CompletionReportScopeRun || meta.Headline != "" || meta.Summary != "" || len(meta.Findings) != 0 || len(meta.Limits) != 0 {
			t.Fatalf("ordinary completion retained document fields: %+v", meta)
		}
	}
	meta := completionReportMeta("implement_synthesis", CompletionReportBinding{RunID: "survey", Phase: "report", PhaseDeliversRunReport: true}, report)
	if meta.Scope != api.CompletionReportScopeRun || meta.Headline == "" || len(meta.Findings) != 1 {
		t.Fatalf("enabled report lost document fields: %+v", meta)
	}
}

func testReportFrame() inject.CoordinatorTurnFrame {
	return inject.CoordinatorTurnFrame{
		RunContext: api.CoordinatorRunContext{RunID: "report-run", CurrentPhase: "report"},
		Runtime:    inject.WorkflowRuntimeSnapshot{ReportDocumentEnabled: true},
	}
}

func TestReviewResultRetainsOfferedPhaseForReplay(t *testing.T) {
	st := &promptLoopTurnState{}
	st.coordinatorFrame.RunContext.RunID = "run"
	st.coordinatorFrame.RunContext.CurrentPhase = "claims"
	original := &api.ToolResult{Tool: "submit_verdict", Feedback: []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID"}}}
	msg := api.Message{ToolResult: original}
	bindReviewResult(&msg, st)
	if msg.WorkflowRunID != "run" || msg.ToolResult.Feedback[0].Details["workflow_phase"] != "claims" {
		t.Fatalf("missing review binding: %+v", msg)
	}
	if original.Feedback[0].Details != nil {
		t.Fatal("binding mutated the invocation's feedback")
	}
}

// The durable row carries the run and phase that offered the call, so repair
// accounting reads them from the transcript rather than the advancing run.
func TestRejectedVerdictPersistsWithOfferedBinding(t *testing.T) {
	var persisted []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		AppendMessages: func(_ context.Context, _ string, rows ...api.Message) error {
			persisted = append(persisted, rows...)
			return nil
		},
	})
	st := &promptLoopTurnState{}
	st.coordinatorFrame.RunContext.RunID = "run"
	st.coordinatorFrame.RunContext.CurrentPhase = "claims"
	msg := api.Message{ID: "result", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "submit_verdict", ToolCallID: "call", AssistantMessageID: "response", Outcome: api.ToolResultOutcomeRejected, Feedback: []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID"}}}}
	var last time.Time
	_, err := toolBatch{loop}.persistClassifiedToolOutcome(t.Context(), "session", &api.Session{ID: "session"}, nil, toolCallOutcome{toolMsg: msg, toolName: "submit_verdict"}, &last, st)
	testutil.FailErr(t, "persist rejected verdict", err)
	for _, row := range persisted {
		if row.ToolResult != nil && row.WorkflowRunID == "run" && row.ToolResult.Feedback[0].Details["workflow_phase"] == "claims" {
			return
		}
	}
	t.Fatalf("persisted rows lost the offered workflow binding: %+v", persisted)
}
