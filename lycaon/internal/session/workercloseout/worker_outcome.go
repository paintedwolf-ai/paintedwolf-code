// Package workercloseout resolves accepted reports and interrupted worker exits.
package workercloseout

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
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
	// SpendLimitReached marks a round fenced at the armed spend limit.
	SpendLimitReached bool
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
	return missingReportOutcome(ctx, childSessionID, agentType, msgs, opts)
}

func FinalizeWorkerSummaryForCanceled(
	ctx context.Context,
	resolver WorkerSummaryResolver,
	hostTurns HostTurnRunner,
	childSessionID, cancelReason string,
	opts WorkerSummaryFinalizeOpts,
) (WorkerSummaryOutcome, error) {
	opts.hostTurns = hostTurns
	childSessionID = strings.TrimSpace(childSessionID)
	cancelReason = strings.TrimSpace(cancelReason)
	ctx = workercontext.WithJob(ctx, opts.WorkerJobID)
	opts = scopeWorkerFinalizeLedger(resolver, opts)

	if resolver != nil {
		messages, err := loadChildMessages(ctx, resolver, childSessionID)
		if err != nil {
			return WorkerSummaryOutcome{}, fmt.Errorf("load canceled worker transcript: %w", err)
		}
		if accepted, _, ok := workercompletion.AcceptedWorkerReport(messages, opts.WorkerJobID); ok {
			return canceledWorkerOutcome(acceptedWorkerOutcome(accepted), cancelReason), nil
		}
	}

	if resolver != nil {
		kickData := map[string]any{"cancel_reason": cancelReason}
		closeout := RenderWorkerKick(ctx, opts.RenderWorkerKick, anchor.InformRenderFor(ctx, anchor.WorkerCancelCloseout, anchor.MatchContext{Surface: "worker", SessionID: childSessionID}), kickData)
		if closeout != "" {
			if _, err := opts.hostTurns.PromptHostTurn(ctx, childSessionID, store.PromptSubmissionOriginWorkerCloseout, closeout); err == nil {
				messages, err := loadChildMessages(ctx, resolver, childSessionID)
				if err != nil {
					return WorkerSummaryOutcome{}, fmt.Errorf("load canceled worker transcript: %w", err)
				}
				if accepted, _, ok := workercompletion.AcceptedWorkerReport(messages, opts.WorkerJobID); ok {
					outcome := acceptedWorkerOutcome(accepted)
					outcome.Provenance = "cancel_complete_leg"
					return canceledWorkerOutcome(outcome, cancelReason), nil
				}
			}
		}
	}
	return WorkerSummaryOutcome{
		Status:     string(api.WorkerSummaryStatusFailed),
		Report:     workercompletion.WorkerCompletionReport{RemainingRisk: AppendRemainingRisk(nil, cancelReason)},
		Provenance: "stopped",
	}, nil
}

func canceledWorkerOutcome(outcome WorkerSummaryOutcome, reason string) WorkerSummaryOutcome {
	outcome.Status = "partial"
	if outcome.Report.DeclaredLegStatus == "" {
		outcome.Report.DeclaredLegStatus = outcome.Report.LegStatus
	}
	if outcome.Report.LegStatus == "" || outcome.Report.LegStatus == "complete" {
		outcome.Report.LegStatus = "partial"
	}
	outcome.Report.RemainingRisk = AppendRemainingRisk(outcome.Report.RemainingRisk, reason)
	return outcome
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

func acceptedWorkerOutcome(accepted *api.WorkerReportAcceptance) WorkerSummaryOutcome {
	report := workercompletion.ReportFromWire(accepted.Report)
	report.DeclaredLegStatus = report.LegStatus
	return WorkerSummaryOutcome{Status: StateFromLegStatus(report.LegStatus), Summary: report.Brief, Report: report, Grounding: accepted.Grounding, Provenance: "complete_leg"}
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

// missingReportOutcome fails a round that ended without an accepted report and
// says why. Its work stays with the job; nothing reports on the worker's behalf.
func missingReportOutcome(ctx context.Context, childSessionID, agentType string, messages []api.Message, opts WorkerSummaryFinalizeOpts) (WorkerSummaryOutcome, error) {
	evaluated, err := workercompletion.EvaluateWorkerSummary(ctx, workercompletion.WorkerSummaryEvalInput{
		AgentType: agentType, MissingReport: !opts.SpendLimitReached, SpendLimitReached: opts.SpendLimitReached,
		ChildSessionID: childSessionID, ChildMessages: messages, ProjectDir: opts.ProjectDir, ProjectRoots: opts.ProjectRoots,
		ActiveRootID: opts.ActiveRootID, WorkspaceCheck: opts.WorkspaceCheck, Hints: opts.WorkflowHints, Ledger: opts.Ledger,
		Pipeline: opts.Pipeline, Reach: opts.Reach,
	})
	if err != nil {
		return WorkerSummaryOutcome{}, err
	}
	provenance := "report_missing"
	if opts.SpendLimitReached {
		provenance = "spend_limit"
	}
	return WorkerSummaryOutcome{
		Status:         string(api.WorkerSummaryStatusFailed),
		HintCode:       evaluated.HintCode,
		PolicyFeedback: evaluated.PolicyFeedback(),
		Grounding:      &evaluated.Grounding,
		Provenance:     provenance,
	}, nil
}
