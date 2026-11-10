// Package workercloseout resolves accepted reports and interrupted worker exits.
package workercloseout

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
)

const WorkerSummaryTooLongCode = "WORKER_SUMMARY_TOO_LONG"

type WorkerSummaryFinalizeOpts struct {
	hostTurns HostTurnRunner
	// WorkerJobID scopes every transcript read.
	WorkerJobID      string
	MaxChars         int
	WorkflowHints    *guidance.HintConfig
	RenderWorkerKick WorkerKickRenderer
	ProjectDir       string
	ProjectRoots     []projectroot.RootRef
	ActiveRootID     string
	AgentType        string
	WorkspaceCheck   workercompletion.WorkspaceChangeChecker
	Ledger           guidance.EvidenceLedgerReader
	// Pipeline is required for blocking worker.report_check Decisions.
	Pipeline        *oar.GuardPipeline
	DecisionPending func(ctx context.Context, childSessionID string) bool
	// Reach is what the worker's latest turn could call; none
	// after a restart, when no live turn result exists.
	Reach toolreach.Request
}

func (o WorkerSummaryFinalizeOpts) decisionPending(ctx context.Context, childSessionID string) bool {
	return o.DecisionPending != nil && o.DecisionPending(ctx, childSessionID)
}

type WorkerSummaryFinalizeBounds interface {
	WorkerSummaryFinalizeOpts(ctx context.Context, sess *api.Session) WorkerSummaryFinalizeOpts
}

// WorkerSummaryOutcome is the host-resolved parent-visible worker completion.
type WorkerSummaryOutcome struct {
	Status         string
	Summary        string
	Report         workercompletion.WorkerCompletionReport
	HintCode       string
	PolicyFeedback *api.WorkerPolicyFeedback
	Grounding      *api.CitationGrounding
	Provenance     string
	// HostAssembled marks citations bound from observed evidence.
	HostAssembled bool
}

// HostTurnRunner runs closeout and synthesis after the primary worker Prompt.
type HostTurnRunner interface {
	PromptHostTurn(ctx context.Context, sessionID string, origin store.PromptSubmissionOrigin, text string) (*promptresult.Result, error)
}

type WorkerSummaryResolver interface {
	GetWorkerJobMessages(ctx context.Context, sessionID, workerJobID string) ([]api.Message, error)
}

func FinalizeWorkerSummaryForChild(
	ctx context.Context,
	resolver WorkerSummaryResolver,
	hostTurns HostTurnRunner,
	childSessionID, agentType string,
	opts WorkerSummaryFinalizeOpts,
) (outcome WorkerSummaryOutcome, err error) {
	opts.hostTurns = hostTurns
	childSessionID = strings.TrimSpace(childSessionID)
	agentType = strings.TrimSpace(agentType)
	ctx = workercontext.WithJob(ctx, opts.WorkerJobID)
	opts = scopeWorkerFinalizeLedger(resolver, opts)
	defer func() {
		// A closeout prompt can itself request a decision.
		if err == nil && opts.decisionPending(ctx, childSessionID) {
			outcome = WorkerSummaryOutcome{Status: string(api.WorkerSummaryStatusNeedsDecision), Provenance: "decision_pending"}
		}
	}()

	// An open decision parks the worker run.
	if opts.decisionPending(ctx, childSessionID) {
		return WorkerSummaryOutcome{
			Status:     string(api.WorkerSummaryStatusNeedsDecision),
			Provenance: "decision_pending",
		}, nil
	}

	var msgs []api.Message
	if resolver != nil {
		loaded, err := loadChildMessages(ctx, resolver, childSessionID)
		if err != nil {
			return WorkerSummaryOutcome{}, fmt.Errorf("load worker finalization transcript: %w", err)
		}
		msgs = loaded
	}
	if accepted, _, ok := workercompletion.AcceptedWorkerReport(msgs, opts.WorkerJobID); ok {
		return acceptedWorkerOutcome(accepted), nil
	}
	if report, ok := workercompletion.SynthesizeCompletionReport(msgs, agentType, workercompletion.WorkerCompletionProof{}); ok {
		return finalizeBoundedReport(ctx, resolver, childSessionID, agentType, "synthesized", report, opts)
	}

	return missingWorkerReportOutcome(ctx, childSessionID, agentType, "none", msgs, opts)
}

func FinalizeWorkerSummaryForCanceled(
	ctx context.Context,
	resolver WorkerSummaryResolver,
	hostTurns HostTurnRunner,
	childSessionID, agentType, cancelReason string,
	opts WorkerSummaryFinalizeOpts,
) (WorkerSummaryOutcome, error) {
	opts.hostTurns = hostTurns
	childSessionID = strings.TrimSpace(childSessionID)
	agentType = strings.TrimSpace(agentType)
	cancelReason = strings.TrimSpace(cancelReason)
	ctx = workercontext.WithJob(ctx, opts.WorkerJobID)
	opts = scopeWorkerFinalizeLedger(resolver, opts)

	if resolver != nil {
		kickData := map[string]any{"cancel_reason": cancelReason}
		closeout := RenderWorkerKick(ctx, opts.RenderWorkerKick, anchor.InformRenderFor(ctx, anchor.WorkerCancelCloseout, anchor.MatchContext{Surface: "worker", SessionID: childSessionID}), kickData)
		if closeout != "" {
			if result, err := opts.hostTurns.PromptHostTurn(ctx, childSessionID, store.PromptSubmissionOriginWorkerCloseout, closeout); err == nil {
				opts.Reach = result.RequestReach()
				if report, ok, err := extractCompleteLegReport(ctx, resolver, childSessionID); err != nil {
					return WorkerSummaryOutcome{}, err
				} else if ok {
					outcome, err := finalizeBoundedReport(ctx, resolver, childSessionID, agentType, "cancel_complete_leg", report, opts)
					outcome.Status = "partial"
					if outcome.Report.DeclaredLegStatus == "" {
						outcome.Report.DeclaredLegStatus = outcome.Report.LegStatus
					}
					if outcome.Report.LegStatus == "" || outcome.Report.LegStatus == "complete" {
						outcome.Report.LegStatus = "partial"
					}
					if cancelReason != "" {
						outcome.Report.RemainingRisk = AppendRemainingRisk(outcome.Report.RemainingRisk, cancelReason)
					}
					return outcome, err
				}

			}
		}
	}

	var msgs []api.Message
	if resolver != nil {
		loaded, err := loadChildMessages(ctx, resolver, childSessionID)
		if err != nil {
			return WorkerSummaryOutcome{}, fmt.Errorf("load worker finalization transcript: %w", err)
		}
		msgs = loaded
	}
	if report, ok := workercompletion.SynthesizeCompletionReport(msgs, agentType, workercompletion.WorkerCompletionProof{}); ok {
		out, err := finalizeBoundedReport(ctx, resolver, childSessionID, agentType, "cancel_synthesized", report, opts)
		out.Status = "partial"
		if cancelReason != "" {
			out.Report.RemainingRisk = AppendRemainingRisk(out.Report.RemainingRisk, cancelReason)
		}
		return out, err
	}

	return missingWorkerReportOutcome(ctx, childSessionID, agentType, "cancel_none", msgs, opts)
}

func scopeWorkerFinalizeLedger(resolver WorkerSummaryResolver, opts WorkerSummaryFinalizeOpts) WorkerSummaryFinalizeOpts {
	if strings.TrimSpace(opts.WorkerJobID) == "" || opts.Ledger == nil {
		return opts
	}
	opts.Ledger = ledgerReaderForWorkerJob(opts.Ledger, resolver, opts.WorkerJobID)
	return opts
}

func AppendRemainingRisk(list []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return list
	}
	for _, existing := range list {
		if strings.TrimSpace(existing) == value {
			return list
		}
	}
	return append(list, value)
}

func extractCompleteLegReport(ctx context.Context, resolver WorkerSummaryResolver, childSessionID string) (workercompletion.WorkerCompletionReport, bool, error) {
	if resolver == nil {
		return workercompletion.WorkerCompletionReport{}, false, nil
	}
	msgs, err := loadChildMessages(ctx, resolver, childSessionID)
	if err != nil {
		return workercompletion.WorkerCompletionReport{}, false, fmt.Errorf("load worker completion transcript: %w", err)
	}
	report, _, ok := workercompletion.LastCompleteLegReport(msgs)
	return report, ok, nil
}

func finalizeBoundedReport(
	ctx context.Context,
	resolver WorkerSummaryResolver,
	childSessionID, agentType, baseProvenance string,
	report workercompletion.WorkerCompletionReport,
	opts WorkerSummaryFinalizeOpts,
) (WorkerSummaryOutcome, error) {
	if strings.TrimSpace(opts.AgentType) != "" {
		agentType = strings.TrimSpace(opts.AgentType)
	}
	messages, err := loadChildMessages(ctx, resolver, childSessionID)
	if err != nil {
		return WorkerSummaryOutcome{}, err
	}
	if accepted, _, ok := workercompletion.AcceptedWorkerReport(messages, opts.WorkerJobID); ok {
		outcome := acceptedWorkerOutcome(accepted)
		outcome.Provenance = baseProvenance
		return outcome, nil
	}
	report.LegStatus = "partial"
	evaluation, err := workercompletion.EvaluateWorkerSummary(ctx, workerReportEvalInput(childSessionID, agentType, report, messages, opts))
	if err != nil {
		return WorkerSummaryOutcome{}, err
	}
	report, provenance, evaluation, err := hostAssembleWorkerExit(ctx, evaluation.Report, opts, childSessionID, evaluation)
	if err != nil {
		return WorkerSummaryOutcome{}, err
	}
	return evaluatedWorkerOutcome(report, evaluation, joinProvenance(baseProvenance, provenance), evaluation.Grounding.HostAssembled), nil
}

func acceptedWorkerOutcome(accepted *api.WorkerReportAcceptance) WorkerSummaryOutcome {
	report := workercompletion.ReportFromWire(accepted.Report)
	report.DeclaredLegStatus = report.LegStatus
	return WorkerSummaryOutcome{Status: StateFromLegStatus(report.LegStatus), Summary: report.Brief, Report: report, Grounding: accepted.Grounding, Provenance: "complete_leg"}
}

func evaluatedWorkerOutcome(report workercompletion.WorkerCompletionReport, evaluation workercompletion.WorkerSummaryEvalResult, provenance string, hostAssembled bool) WorkerSummaryOutcome {
	report.DeclaredLegStatus = report.LegStatus
	status := StateFromLegStatus(report.LegStatus)
	if status == "" {
		status = "complete"
	}
	if evaluation.Status == "partial" {
		status = "partial"
	}
	if status == string(api.WorkerSummaryStatusPartial) && report.LegStatus == "complete" {
		report.LegStatus = "partial"
	}
	return WorkerSummaryOutcome{Status: status, Summary: report.Brief, Report: report, HintCode: evaluation.HintCode, PolicyFeedback: evaluation.PolicyFeedback(), Grounding: &evaluation.Grounding, Provenance: provenance, HostAssembled: hostAssembled}
}

func joinProvenance(base, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return base
	}
	if base == "" {
		return extra
	}
	return base + "+" + extra
}

func workerSummaryMaxChars(opts WorkerSummaryFinalizeOpts) int {
	if opts.MaxChars > 0 {
		return opts.MaxChars
	}
	return limits.DefaultWorkerSummaryMaxChars
}

func workerReportEvalInput(childSessionID, agentType string, report workercompletion.WorkerCompletionReport, messages []api.Message, opts WorkerSummaryFinalizeOpts) workercompletion.WorkerSummaryEvalInput {
	return workercompletion.WorkerSummaryEvalInput{AgentType: agentType, MaxChars: workerSummaryMaxChars(opts), Report: report, ChildSessionID: childSessionID, ChildMessages: messages, ProjectDir: opts.ProjectDir, ProjectRoots: opts.ProjectRoots, ActiveRootID: opts.ActiveRootID, WorkspaceCheck: opts.WorkspaceCheck, Hints: opts.WorkflowHints, Ledger: opts.Ledger, Pipeline: opts.Pipeline, Reach: opts.Reach}
}

func missingWorkerReportOutcome(ctx context.Context, childSessionID, agentType, provenance string, messages []api.Message, opts WorkerSummaryFinalizeOpts) (WorkerSummaryOutcome, error) {
	evaluated, err := workercompletion.EvaluateWorkerSummary(ctx, workercompletion.WorkerSummaryEvalInput{AgentType: agentType, MissingReport: true, ChildSessionID: childSessionID, ChildMessages: messages, ProjectDir: opts.ProjectDir, ProjectRoots: opts.ProjectRoots, ActiveRootID: opts.ActiveRootID, WorkspaceCheck: opts.WorkspaceCheck, Hints: opts.WorkflowHints, Ledger: opts.Ledger, Pipeline: opts.Pipeline, Reach: opts.Reach})
	if err != nil {
		return WorkerSummaryOutcome{}, err
	}
	return WorkerSummaryOutcome{Status: "partial", HintCode: evaluated.HintCode, PolicyFeedback: evaluated.PolicyFeedback(), Grounding: &evaluated.Grounding, Provenance: provenance}, nil
}

func StateFromLegStatus(status string) string {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "complete":
		return string(api.WorkerSummaryStatusComplete)
	case "partial", "blocked":
		return string(api.WorkerSummaryStatusPartial)
	default:
		return ""
	}
}
