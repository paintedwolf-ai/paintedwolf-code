package workercompletion_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerReportSubmissionAcceptsOnlyValidatedReports(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	for _, tc := range []struct {
		name, brief, code string
		max               int
	}{
		{name: "valid partial", brief: "Inspected the bounded scope"},
		{name: "empty brief", code: "WORKER_TURN_NO_PROSE"},
		{name: "over budget", brief: "Longer than the measured limit", max: 8, code: "WORKER_SUMMARY_TOO_LONG"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{AgentType: "security-reviewer", ChildSessionID: t.Name(), MaxChars: tc.max, Report: workercompletion.WorkerCompletionReport{LegStatus: "partial", Brief: tc.brief}})
			receipt, err := workercompletion.AcceptWorkerReport(t.Context(), "job", input)
			if tc.code == "" {
				testutil.FailErr(t, "accept worker report", err)
				if receipt == nil || receipt.WorkerJobID != "job" || receipt.Report.Brief != tc.brief || receipt.Grounding == nil {
					t.Fatalf("acceptance = %+v", receipt)
				}
				return
			}
			reject := toolrejection.AsToolReject(err)
			if receipt != nil || reject == nil || reject.Code != tc.code {
				t.Fatalf("receipt=%+v error=%v reject=%+v", receipt, err, reject)
			}
			refusal, ok := guidance.RefusalFromError(err)
			if !ok || refusal.Facts.Outcome != api.ToolResultOutcomeRejected || refusal.Code() != tc.code || len(refusal.Facts.Feedback) != 1 {
				t.Fatalf("missing structured rejection: %+v", refusal)
			}
			if tc.max > 0 && refusal.Facts.Feedback[0].Details["max_chars"] != tc.max {
				t.Fatalf("recovery lost measured limit: %+v", refusal.Facts.Feedback)
			}
		})
	}
}

func TestWorkerReportSubmissionPreservesOperationalFailure(t *testing.T) {
	failure := errors.New("ledger unavailable")
	input := workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{AgentType: "security-reviewer", ChildSessionID: t.Name(), Report: workercompletion.WorkerCompletionReport{LegStatus: "partial", Brief: "Observed result"}, Ledger: failedWorkerLedger{err: failure}})
	receipt, err := workercompletion.AcceptWorkerReport(t.Context(), "job", input)
	if receipt != nil || !errors.Is(err, failure) || toolrejection.AsToolReject(err) != nil {
		t.Fatalf("operational failure lost: %+v %v", receipt, err)
	}
	receipt, err = workercompletion.AcceptWorkerReport(t.Context(), "", input)
	if receipt != nil || err == nil {
		t.Fatalf("accepted unidentified report: %+v %v", receipt, err)
	}
}

func TestPartialWorkerReportDoesNotRequireCompletedImplementation(t *testing.T) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	for _, status := range []string{"partial", "blocked", "complete"} {
		t.Run(status, func(t *testing.T) {
			input := workerSummaryEval(t, workercompletion.WorkerSummaryEvalInput{AgentType: "implementer", ChildSessionID: t.Name(), Report: workercompletion.WorkerCompletionReport{LegStatus: status, Brief: "No implementation was applied", RemainingRisk: []string{"Assigned change remains incomplete"}}})
			receipt, err := workercompletion.AcceptWorkerReport(t.Context(), "job", input)
			if status == "complete" {
				rejection := toolrejection.AsToolReject(err)
				if receipt != nil || rejection == nil || rejection.Code != "WORKER_SUMMARY_NO_ARTIFACT" {
					t.Fatalf("unsupported completion accepted: receipt=%+v err=%v", receipt, err)
				}
				return
			}
			testutil.FailErr(t, "accept incomplete worker result", err)
			if receipt == nil || receipt.Report.LegStatus != status {
				t.Fatalf("lost incomplete disposition: %+v", receipt)
			}
		})
	}
}
