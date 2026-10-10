package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

// TerminalNotice publishes a worker cancellation to its parent.
type TerminalNotice interface {
	Terminal(ctx context.Context, parentSessionID, jobID string)
}

// completeGracefulStop records the child closeout and parent cancellation.
func (e *LocalWorkerExecutor) completeGracefulStop(
	ctx context.Context,
	task api.WorkerTask,
	child *api.Session,
	cancelReason string,
	finalizeOpts workercloseout.WorkerSummaryFinalizeOpts,
) (api.WorkerResult, error) {
	if e.Sessions == nil || child == nil {
		return api.WorkerResult{}, fmt.Errorf("worker session not configured")
	}
	parentID := strings.TrimSpace(task.ParentSessionID)
	if parentID == "" {
		return api.WorkerResult{}, fmt.Errorf("worker task missing parent_session_id")
	}

	outcome, err := workercloseout.FinalizeWorkerSummaryForCanceled(ctx, e.Transcripts, e.Prompts, child.ID, task.AgentType, cancelReason, finalizeOpts)
	if err != nil {
		return api.WorkerResult{}, err
	}

	report := BuildChangeReport(ctx, task, e.Reports)
	summary := strings.TrimSpace(outcome.Summary)
	if summary == "" {
		summary = cancellationSummary(task, cancelReason, report)
	}
	result := api.WorkerResult{
		Status:         "canceled",
		HintCode:       outcome.HintCode,
		PolicyFeedback: outcome.PolicyFeedback,
		Grounding:      outcome.Grounding,
		Response:       "canceled",
		Summary:        summary,
		ChangeReport:   &report,
	}

	completionReport := outcome.Report
	if strings.TrimSpace(completionReport.Brief) == "" {
		completionReport.Brief = summary
	}
	if len(completionReport.ObjectivesMet) == 0 {
		completionReport.ObjectivesMet = []string{"Worker leg canceled"}
	}
	completionReport.LegStatus = "partial"
	result.CompletionReport = workercompletion.ReportWire(completionReport)

	if err := e.WorkerCancellations.Append(ctx, parentID, workeroutcomes.CancellationInput{
		JobID:            task.ID,
		AgentType:        task.AgentType,
		ChildSessionID:   child.ID,
		Reason:           cancelReason,
		Report:           report,
		CompletionReport: completionReport,
		Result:           result,
	}); err != nil {
		slog.WarnContext(ctx, "graceful stop cancellation append deferred", "job_id", task.ID, "parent_session_id", parentID, "err", err)
	}

	return result, nil
}

type WorkerCancellationProjection interface {
	Append(context.Context, string, workeroutcomes.CancellationInput) error
}
type WorkerCancellationRuntime interface {
	WorkerCancellationProjection
	StopRuntime(context.Context, string) error
}
