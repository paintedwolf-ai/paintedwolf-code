package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerFinishRequiresCurrentJobAcceptanceWhileToolsRemain(t *testing.T) {
	for _, tc := range []struct {
		name        string
		invoke      bool
		acceptedJob string
		outcome     api.ToolResultOutcome
		blocked     bool
	}{
		{name: "prose only", invoke: true, blocked: true},
		{name: "accepted", invoke: true, acceptedJob: "job-worker", outcome: api.ToolResultOutcomeCompleted},
		{name: "other job", invoke: true, acceptedJob: "other", outcome: api.ToolResultOutcomeCompleted, blocked: true},
		{name: "rejected", invoke: true, acceptedJob: "job-worker", outcome: api.ToolResultOutcomeRejected, blocked: true},
		{name: "budget exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, child, task, history := workerSourceEvidenceCloseoutHarness(t)
			if tc.acceptedJob != "" {
				history = append(history, api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
					Tool: "complete_leg", Outcome: tc.outcome,
					WorkerReport: &api.WorkerReportAcceptance{WorkerID: tc.acceptedJob, Report: &api.WorkerCompletionReport{LegStatus: "partial", Brief: "Observed result"}, Grounding: &api.CitationGrounding{}},
				}})
			}
			reject, blocked := mgr.Coordinator.Guards.BeforeFinish(workercontext.WithJob(t.Context(), task.ID), child, history, "", "Worker prose", guidance.CloseoutDraft{}, "", true, nil, tc.invoke)
			if blocked != tc.blocked || (blocked && (reject == nil || reject.Code() != "WORKER_REPORT_REQUIRED")) {
				t.Fatalf("finish blocked=%v refusal=%v; want blocked=%v", blocked, reject, tc.blocked)
			}
		})
	}
}
