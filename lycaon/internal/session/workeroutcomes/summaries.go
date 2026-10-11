package workeroutcomes

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

type workerSummaryDraft struct {
	input         SummaryInput
	task          *api.WorkerTask
	childMessages []api.Message
	report        workercompletion.WorkerCompletionReport
	summary       string
	hintCode      string
	status        string
	body          string
}

// Append adds a terminal worker row to the parent transcript.
func (m *Summaries) Append(ctx context.Context, parentID string, in SummaryInput) (string, error) {
	if m == nil || m.store == nil {
		return "", fmt.Errorf("worker summary service unavailable")
	}
	in.JobID = strings.TrimSpace(in.JobID)
	in.ChildSessionID = strings.TrimSpace(in.ChildSessionID)
	in.AgentType = strings.TrimSpace(in.AgentType)
	if in.JobID == "" || in.ChildSessionID == "" || in.AgentType == "" {
		return "", fmt.Errorf("worker summary identity required")
	}
	childMessages, err := m.store.GetWorkerJobMessages(ctx, in.ChildSessionID, in.JobID)
	if err != nil {
		return "", fmt.Errorf("load worker summary transcript: %w", err)
	}
	var task *api.WorkerTask
	if strings.TrimSpace(in.JobID) != "" && m.tasks != nil {
		if loaded, ok := m.tasks.Get(in.JobID); ok && loaded != nil {
			task = loaded
		}
	}
	report := in.Report
	report.Normalize()
	summary := strings.TrimSpace(report.Brief)
	if summary == "" {
		summary = strings.TrimSpace(in.Summary)
	}
	hintCode := strings.TrimSpace(in.HintCode)
	status := strings.TrimSpace(in.Status)
	if status == "" && report.LegStatus != "" {
		status = workercloseout.StateFromLegStatus(report.LegStatus)
	}
	if status == "" {
		if summary != "" {
			status = string(api.WorkerSummaryStatusComplete)
		} else {
			status = string(api.WorkerSummaryStatusPartial)
		}
	}
	parent, err := m.store.Get(ctx, parentID)
	if err != nil {
		return "", fmt.Errorf("load worker summary parent: %w", err)
	}
	maxChars := m.limits.Compaction(ctx, parent).MaxWorkerSummaryChars
	if maxChars <= 0 {
		maxChars = compaction.DefaultCompactionConfig().MaxWorkerSummaryChars
	}
	feedback := in.PolicyFeedback
	if feedback != nil && feedback.Code != hintCode {
		return "", fmt.Errorf("worker policy feedback code does not match hint_code")
	}
	if !in.ReportEvaluated && feedback == nil && status != string(api.WorkerSummaryStatusFailed) {
		if report.Brief == "" {
			report.Brief = summary
		}
		eval, err := workercompletion.EvaluateWorkerSummary(ctx, workercompletion.WorkerSummaryEvalInput{MaxChars: maxChars, AgentType: in.AgentType, Report: report, MissingReport: summary == "" && report.LegStatus == "", ChildSessionID: in.ChildSessionID, ChildMessages: childMessages, ProjectDir: in.ProjectDir, ProjectRoots: in.ProjectRoots, ActiveRootID: in.ActiveRootID, WorkspaceCheck: m.workspaceCheck, Hints: m.workflowHints, Ledger: workercloseout.LedgerForMessages(m.store, childMessages), Pipeline: m.oarPipeline})
		if err != nil {
			return "", err
		}
		if in.GroundingOut != nil {
			*in.GroundingOut = eval.Grounding
		}
		if eval.Status == "partial" {
			status = "partial"
		}
		if hintCode == "" {
			hintCode = eval.HintCode
			feedback = eval.PolicyFeedback()
		}
		summary = eval.Summary
	}
	budget := m.budget.BudgetForTask(ctx, task)
	if exhaustCode, exhaustData := BudgetExhaustedHint(status, task, childMessages, budget); exhaustCode != "" && feedback == nil {
		hintCode = exhaustCode
		if m.workflowHints != nil {
			formatted, err := guidance.FormatWorkerSummaryFeedback(m.workflowHints, hintCode, exhaustData)
			if err == nil && strings.TrimSpace(formatted) != "" {
				summary = formatted
			}
		}
	}
	body := ""
	if feedback != nil {
		rendered, err := guidance.RenderPolicyCopy(ctx, feedback.Code, feedback.Effect, feedback.Copy)
		if err != nil {
			return "", fmt.Errorf("render frozen worker policy feedback: %w", err)
		}
		summary = strings.TrimSpace(rendered)
	} else if summary == "" && hintCode != "" && m.workflowHints != nil {
		formatted, err := guidance.FormatWorkerSummaryFeedback(m.workflowHints, hintCode, nil)
		if err != nil {
			return "", err
		}
		body = strings.TrimSpace(formatted)
	}
	return m.completeWorkerSummary(ctx, parentID, workerSummaryDraft{
		input: in, task: task, childMessages: childMessages, report: report,
		summary: summary, hintCode: hintCode, status: status, body: body,
	})
}

func (m *Summaries) completeWorkerSummary(
	ctx context.Context,
	parentID string,
	draft workerSummaryDraft,
) (string, error) {
	// Decision cards require a committed suspension.
	var decisionReq *api.WorkerDecisionRequest
	completionStatus := draft.input.Status
	if draft.task != nil && draft.task.Result != nil {
		completionStatus = draft.task.Result.Status
	}
	if m.decisions != nil {
		dec, ok, err := m.decisions.GetByJob(ctx, draft.input.JobID)
		if err != nil {
			return "", fmt.Errorf("load worker decision: %w", err)
		}
		if ok {
			completionStatus = string(api.WorkerSummaryStatusNeedsDecision)
			req := dec
			decisionReq = &req
		}
	}
	if completionStatus == string(api.WorkerSummaryStatusNeedsDecision) {
		if decisionReq == nil && m.decisions == nil {
			return "", fmt.Errorf("suspended worker decision store unavailable")
		}
		if decisionReq == nil {
			return "", fmt.Errorf("suspended worker decision missing")
		}
		draft.status = string(api.WorkerSummaryStatusNeedsDecision)
		draft.hintCode = ""
		draft.summary, draft.body = FormatWorkerDecision(draft.input.AgentType, *decisionReq)
	}
	draft.status = workercompletion.NormalizeWorkerCompletionState(draft.status)
	if draft.status == "" {
		return "", fmt.Errorf("worker summary status invalid")
	}
	proof := m.Proof(ctx, draft.task, draft.childMessages, draft.input.ProjectDir)
	draft.report = workercompletion.EnrichWorkerCompletionReport(draft.report, proof, draft.status)
	draft.report = workercompletion.AnnotateWorkerReportForBranch(draft.report, draft.task)
	if decisionReq == nil {
		draft.status = string(m.overlayStatus(
			ctx, api.WorkerSummaryStatus(draft.status), draft.task,
		))
	}
	digest := workercompletion.FormatWorkerDigest(
		draft.input.AgentType, draft.input.JobID, draft.status, draft.summary, proof, draft.report,
	)
	if decisionReq != nil {
		digest = workercompletion.AppendWorkerDecisionDigest(digest, *decisionReq)
	}
	env := workercompletion.WorkerCompletionEnvelope{
		JobID:           draft.input.JobID,
		ChildSessionID:  draft.input.ChildSessionID,
		AgentType:       draft.input.AgentType,
		State:           draft.status,
		HintCode:        draft.hintCode,
		Summary:         draft.summary,
		Body:            draft.body,
		Digest:          digest,
		Proof:           proof,
		Report:          draft.report,
		DecisionRequest: decisionReq,
	}
	if draft.task != nil {
		if ms := strings.TrimSpace(string(draft.task.MergeStatus)); ms != "" {
			env.MergeStatus = ms
		} else if m.overlayPending(ctx, draft.task) {
			env.MergeStatus = string(api.WorkerMergeStatusPending)
		}
	}
	delivery, err := m.delivery.Evaluate(ctx, env)
	if err != nil {
		return "", err
	}
	env = delivery.Envelope
	draft.status, digest = env.State, env.Digest
	if delivery.Blocked {
		draft.input.GroundingOut = nil
		draft.childMessages = nil
	}
	envelope := workercompletion.FormatWorkerCompletionEnvelope(env)
	if strings.TrimSpace(draft.input.JobID) != "" && strings.TrimSpace(digest) != "" {
		m.digests.Put(draft.input.JobID, digest)
	}
	ws := &api.WorkerSummaryMeta{
		SourceContext:  sourceref.ForResponse(draft.childMessages, envelope),
		DelegationID:   draft.input.DelegationID,
		LegID:          draft.input.LegID,
		WorkerID:       draft.input.JobID,
		ChildSessionID: draft.input.ChildSessionID,
		AgentType:      draft.input.AgentType,
		Status:         api.WorkerSummaryStatus(draft.status),
		Grounding:      workercompletion.CitationGroundingWire(draft.input.GroundingOut),
		Envelope:       envelope,
	}
	if err := m.cards.Project(ctx, parentID, draft.input.JobID, ws); err != nil {
		return "", err
	}
	if err := MergeWorkerUntrustedIntoParent(ctx, m.store, parentID, draft.input.ChildSessionID); err != nil {
		return "", fmt.Errorf("merge worker untrusted-content state: %w", err)
	}
	if err := MergeWorkerSecretExposureIntoParent(ctx, m.store, parentID, draft.input.ChildSessionID); err != nil {
		return "", fmt.Errorf("merge worker secret-exposure state: %w", err)
	}
	// Completed child transcripts remain durable; turn state does not.
	_ = m.resources.DisposeRuntime(ctx, draft.input.ChildSessionID)
	// Worker events refresh the parent without a session-status edge.
	if m.grounding != nil && draft.input.DelegationID != "" {
		m.grounding.Reset(parentID)
	}
	return draft.status, nil
}

// Tags returns tagged worker summaries without prose for grounding.
func (m *Summaries) Tags(ctx context.Context, sessionID string) ([]api.WorkerSummaryMeta, error) {
	msgs, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var tags []api.WorkerSummaryMeta
	for _, msg := range msgs {
		if msg.WorkerSummary == nil {
			continue
		}
		tag := *msg.WorkerSummary
		tag.DelegationID = strings.TrimSpace(tag.DelegationID)
		tag.LegID = strings.TrimSpace(tag.LegID)
		tag.ChildSessionID = strings.TrimSpace(tag.ChildSessionID)
		tags = append(tags, tag)
	}
	return tags, nil
}
