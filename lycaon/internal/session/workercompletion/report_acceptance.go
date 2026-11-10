package workercompletion

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

// AcceptWorkerReport freezes the submission's report and grounding decision.
func AcceptWorkerReport(ctx context.Context, jobID string, input WorkerSummaryEvalInput) (*api.WorkerReportAcceptance, error) {
	if jobID == "" {
		return nil, fmt.Errorf("worker report requires a job identity")
	}
	evaluated, err := EvaluateWorkerSummary(ctx, input)
	if err != nil {
		return nil, err
	}
	if evaluated.HintCode != "" {
		text, err := evaluated.FormatFeedback(ctx, input.Hints)
		if err != nil {
			return nil, err
		}
		cause := &toolrejection.ToolReject{Code: evaluated.HintCode, Data: evaluated.HintData}
		return nil, guidance.NewRefusal(evaluated.HintCode, text).WithPolicyCopy(evaluated.HintCopy).WithDetails(evaluated.HintData, nil).WithCause(cause)
	}
	return &api.WorkerReportAcceptance{WorkerID: jobID, Report: ReportWire(evaluated.Report), Grounding: &evaluated.Grounding}, nil
}
