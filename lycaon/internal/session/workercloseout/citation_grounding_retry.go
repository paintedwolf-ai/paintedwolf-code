package workercloseout

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

func citationGroundingMaxRetries(opts WorkerSummaryFinalizeOpts) int {
	if opts.MaxGroundingRetries > 0 {
		return opts.MaxGroundingRetries
	}
	return limits.DefaultWorkerGroundingRetries
}

func boundCitationGroundingReport(
	ctx context.Context,
	resolver WorkerSummaryResolver,
	childSessionID, agentType string,
	report workercompletion.WorkerCompletionReport,
	opts WorkerSummaryFinalizeOpts,
) (workercompletion.WorkerCompletionReport, string, workercompletion.WorkerSummaryEvalResult, error) {
	report.Normalize()

	msgs, err := loadChildMessages(ctx, resolver, childSessionID)
	if err != nil {
		return report, "", workercompletion.WorkerSummaryEvalResult{}, fmt.Errorf("load worker report transcript: %w", err)
	}
	// Supersede rejected reports in place.
	_, rejectedReportID, _ := workercompletion.LastCompleteLegReport(msgs)

	evalInput := workercompletion.WorkerSummaryEvalInput{
		AgentType:      agentType,
		MaxChars:       workerSummaryMaxChars(opts),
		Report:         report,
		ChildSessionID: childSessionID,
		ChildMessages:  msgs,
		ProjectDir:     opts.ProjectDir,
		ProjectRoots:   opts.ProjectRoots,
		ActiveRootID:   opts.ActiveRootID,
		WorkspaceCheck: opts.WorkspaceCheck,
		Hints:          opts.WorkflowHints,
		Ledger:         opts.Ledger,
		Pipeline:       opts.Pipeline,
	}
	eval, err := workercompletion.EvaluateWorkerSummary(ctx, evalInput)
	if err != nil {
		return report, "", workercompletion.WorkerSummaryEvalResult{}, err
	}
	report = eval.Report
	if eval.Status == "complete" {
		return report, "", eval, nil
	}
	if !opts.WorkflowHints.IsInSessionRetry(eval.HintCode) {
		return report, "grounding_blocked", eval, nil
	}

	maxRetries := citationGroundingMaxRetries(opts)
	if maxRetries <= 0 || resolver == nil || opts.WorkflowHints == nil {
		return report, "grounding_blocked", eval, nil
	}

	prevKey := guidance.GroundingOffenderKey(eval.HintCode, eval.HintData)
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if opts.decisionPending(ctx, childSessionID) {
			return report, "decision_pending", eval, nil
		}
		reject, err := eval.FormatFeedback(ctx, opts.WorkflowHints)
		if err != nil {
			return report, "", workercompletion.WorkerSummaryEvalResult{}, fmt.Errorf("render worker grounding feedback: %w", err)
		}
		if strings.TrimSpace(reject) == "" {
			return report, "grounding_blocked", eval, nil
		}
		kickData := map[string]any{
			"attempt":                  attempt,
			"max_attempts":             maxRetries,
			"worker_summary_max_chars": workerSummaryMaxChars(opts),
		}
		header := RenderWorkerKick(ctx, opts.RenderWorkerKick, anchor.InformRenderFor(ctx, anchor.WorkerCitationGrounding, anchor.MatchContext{Surface: "worker", SessionID: childSessionID}), kickData)
		if header == "" {
			return report, "grounding_blocked", eval, nil
		}
		prompt := header + "\n\n" + reject
		if err := supersedeWorkerReport(ctx, resolver, childSessionID, rejectedReportID); err != nil {
			return report, "", workercompletion.WorkerSummaryEvalResult{}, err
		}
		retryStart := len(msgs)
		if _, err := opts.hostTurns.PromptHostTurn(ctx, childSessionID, store.PromptSubmissionOriginGroundingRetry, prompt); err != nil {
			return hostAssembleWorkerExit(ctx, report, opts, childSessionID, eval)
		}

		msgs, err = loadChildMessages(ctx, resolver, childSessionID)
		if err != nil {
			return report, "", workercompletion.WorkerSummaryEvalResult{}, fmt.Errorf("load worker retry transcript: %w", err)
		}
		// Parse only rows produced by this retry.
		parsed, reportID, ok := workercompletion.LastCompleteLegReport(msgs[min(retryStart, len(msgs)):])
		if !ok {
			if len(msgs) <= retryStart {
				slog.WarnContext(ctx, "worker grounding retry produced no model turn", "session", childSessionID, "attempt", attempt)
			}
			return hostAssembleWorkerExit(ctx, report, opts, childSessionID, eval)
		}

		rejectedReportID = reportID
		parsed.Normalize()
		report = parsed

		evalInput.Report = report
		evalInput.ChildMessages = msgs
		eval, err = workercompletion.EvaluateWorkerSummary(ctx, evalInput)
		if err != nil {
			return report, "", workercompletion.WorkerSummaryEvalResult{}, err
		}
		report = eval.Report
		if eval.Status == "complete" {
			return report, joinProvenance("grounding_retry", fmt.Sprintf("attempt_%d", attempt)), eval, nil
		}
		if !opts.WorkflowHints.IsInSessionRetry(eval.HintCode) {
			return report, joinProvenance("grounding_retry", fmt.Sprintf("attempt_%d", attempt)), eval, nil
		}
		key := guidance.GroundingOffenderKey(eval.HintCode, eval.HintData)
		if key == prevKey && !guidance.GroundingRetryBypassesStuckDetection(eval.HintCode) {
			return hostAssembleWorkerExit(ctx, report, opts, childSessionID, eval)
		}
		prevKey = key
	}

	return hostAssembleWorkerExit(ctx, report, opts, childSessionID, eval)
}

const workerGroundingHostAssembledProvenance = "grounding_host_assembled"

// hostAssembleWorkerExit binds observed evidence after retry exhaustion.
func hostAssembleWorkerExit(
	ctx context.Context,
	report workercompletion.WorkerCompletionReport,
	opts WorkerSummaryFinalizeOpts,
	childSessionID string,
	blocked workercompletion.WorkerSummaryEvalResult,
) (workercompletion.WorkerCompletionReport, string, workercompletion.WorkerSummaryEvalResult, error) {
	report, assembled, err := bindWorkerObservedSample(ctx, report, opts, childSessionID)
	if err != nil {
		return report, "", workercompletion.WorkerSummaryEvalResult{}, err
	}
	if !assembled {
		return report, "grounding_host_assembly_unavailable", blocked, nil
	}
	blocked.Grounding.HostAssembled = true
	return report, workerGroundingHostAssembledProvenance, blocked, nil
}

type workerReportSuperseder interface {
	SupersedeWorkerReport(ctx context.Context, sessionID, messageID string) error
}

func supersedeWorkerReport(ctx context.Context, resolver WorkerSummaryResolver, childSessionID, messageID string) error {
	if strings.TrimSpace(messageID) == "" {
		return nil
	}
	superseder, ok := resolver.(workerReportSuperseder)
	if !ok {
		return nil
	}
	if err := superseder.SupersedeWorkerReport(ctx, childSessionID, messageID); err != nil {
		return fmt.Errorf("supersede worker completion: %w", err)
	}
	return nil
}

func loadChildMessages(ctx context.Context, resolver WorkerSummaryResolver, childSessionID string) ([]api.Message, error) {
	if resolver == nil {
		return nil, fmt.Errorf("session: nil worker summary resolver")
	}
	jobID := workercontext.Job(ctx)
	if jobID == "" {
		return nil, fmt.Errorf("session: worker job id required")
	}
	return resolver.GetWorkerJobMessages(ctx, childSessionID, jobID)
}
