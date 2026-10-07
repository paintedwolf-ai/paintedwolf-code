package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerSummaryInput is metadata for a parent-visible worker summary.
type WorkerSummaryInput struct {
	Summary         string
	Report          workercompletion.WorkerCompletionReport
	DelegationID    string
	LegID           string
	JobID           string
	AgentType       string
	ChildSessionID  string
	ParentSessionID string
	ProjectDir      string
	ProjectRoots    []projectroot.RootRef
	ActiveRootID    string
	ProjectID       string
	Status          string
	HintCode        string
	PolicyFeedback  *api.WorkerPolicyFeedback
	ReportEvaluated bool
	CompletedAt     *time.Time
	GroundingOut    *api.CitationGrounding
}

type workerSummaryDraft struct {
	input         WorkerSummaryInput
	task          *api.WorkerTask
	childMessages []api.Message
	report        workercompletion.WorkerCompletionReport
	summary       string
	hintCode      string
	status        string
	body          string
}

// SpawnChild creates a child session whose messages stay isolated from the parent.
func (m *Manager) SpawnChild(ctx context.Context, parentID string, req api.SpawnChildRequest) (*api.Session, error) {
	parent, err := m.store.Get(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Prompt) != "" {
		req.Prompt = guidance.StripHostBlocks(req.Prompt)
	}
	var child *api.Session
	err = m.WithSessionTreeAdmission(ctx, parentID, func() error {
		parentUntrusted, untrustedErr := m.store.SessionUntrustedContentResult(ctx, parentID)
		if untrustedErr != nil {
			return fmt.Errorf("read parent untrusted-content state: %w", untrustedErr)
		}
		parentSecret, exposureErr := m.store.SessionSecretExposure(ctx, parentID)
		if exposureErr != nil {
			return fmt.Errorf("read parent secret-exposure state: %w", exposureErr)
		}
		var createErr error
		child, createErr = m.store.CreateChild(ctx, parent, req)
		if createErr != nil {
			return createErr
		}
		rollbackChild := func(cause error) error {
			deleteErr := m.store.Delete(context.WithoutCancel(ctx), child.ID)
			child = nil
			if deleteErr != nil {
				return errors.Join(cause, fmt.Errorf("remove partially initialized child: %w", deleteErr))
			}
			return cause
		}
		if err := m.assignWorkerModel(ctx, parent, child); err != nil {
			return rollbackChild(err)
		}
		if parentUntrusted {
			if err := m.store.SeedUntrustedContent(ctx, child.ID); err != nil {
				return rollbackChild(fmt.Errorf("inherit untrusted-content state: %w", err))
			}
			child.UntrustedContent = true
		}
		if parentSecret {
			if err := m.store.SeedSecretExposure(ctx, child.ID); err != nil {
				return rollbackChild(fmt.Errorf("inherit secret-exposure state: %w", err))
			}
		}
		m.injectWorkerKickOnSpawn(ctx, child)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return child, nil
}

// A worker keeps one pool assignment across completions and host restarts.
func (m *Manager) assignWorkerModel(ctx context.Context, parent, child *api.Session) error {
	if m.llmSvc == nil || m.llmSvc.Router == nil {
		return nil
	}
	selection, err := m.llmSvc.Router.WithOverlayRoots(m.overlayRootPaths(ctx, parent)).Select(ctx)
	if err != nil {
		return fmt.Errorf("select worker model: %w", err)
	}
	if err := m.store.UpdateSession(ctx, child.ID, func(s *api.Session) {
		s.ProviderID, s.Model = selection.ProviderID, selection.Model
	}); err != nil {
		return fmt.Errorf("save worker model: %w", err)
	}
	child.ProviderID, child.Model = selection.ProviderID, selection.Model
	return nil
}

// SetWorkerMaxToolLoops stores the child worker loop cap.
func (m *Manager) SetWorkerMaxToolLoops(ctx context.Context, childSessionID string, maxToolLoops int) error {
	if m == nil || maxToolLoops <= 0 {
		return nil
	}
	childSessionID = strings.TrimSpace(childSessionID)
	if childSessionID == "" {
		return fmt.Errorf("child session id required")
	}
	return m.store.UpdateSession(ctx, childSessionID, func(s *api.Session) {
		s.MaxToolLoops = maxToolLoops
	})
}

func (m *Manager) injectWorkerKickOnSpawn(ctx context.Context, child *api.Session) {
	if m == nil || child == nil || m.prompts == nil {
		return
	}
	data := map[string]string{
		"agent_type": strings.TrimSpace(child.AgentType),
		"leg_id":     "",
		"phase_id":   "",
	}
	if m.workerContext != nil {
		if legCtx, err := m.workerContext.BuildWorkerPromptContext(child.ID, child); err == nil {
			data["agent_type"] = strings.TrimSpace(legCtx.AgentType)
			data["leg_id"] = strings.TrimSpace(legCtx.LegID)
			data["phase_id"] = strings.TrimSpace(legCtx.PhaseID)
		}
	}
	m.EmitEager(ctx, child.ID, workerSpawnAnchor(data["leg_id"]), data)
}

func workerSpawnAnchor(legID string) anchor.ID {
	if strings.TrimSpace(legID) != "" {
		return anchor.WorkerLegStarted
	}
	return anchor.WorkerTaskStarted
}

// AppendWorkerSummary adds a terminal worker row to the parent transcript.
func (m *Manager) AppendWorkerSummary(ctx context.Context, parentID string, in WorkerSummaryInput) (string, error) {
	if m == nil || m.store == nil {
		return "", fmt.Errorf("session manager unavailable")
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
	if strings.TrimSpace(in.JobID) != "" && m.workerQueue != nil {
		if loaded, ok := m.workerQueue.Get(in.JobID); ok && loaded != nil {
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
	maxChars := m.liveCompactionConfigFor(ctx, parent).MaxWorkerSummaryChars
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
	budget := m.workerToolBudgetForTask(ctx, task)
	if exhaustCode, exhaustData := maybeWorkerBudgetExhaustedHint(status, task, childMessages, budget); exhaustCode != "" && feedback == nil {
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

func (m *Manager) completeWorkerSummary(
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
	proof := m.workerValidationProof(ctx, draft.task, draft.childMessages, draft.input.ProjectDir)
	draft.report = workercompletion.EnrichWorkerCompletionReport(draft.report, proof, draft.status)
	draft.report = workercompletion.AnnotateWorkerReportForBranch(draft.report, draft.task)
	if decisionReq == nil {
		draft.status = string(ResolveWorkerSummaryStatus(
			ctx, api.WorkerSummaryStatus(draft.status), draft.task,
		))
	}
	digest := workercompletion.FormatWorkerDigest(
		draft.input.AgentType, draft.input.JobID, draft.status, draft.summary, proof, draft.report,
	)
	if decisionReq != nil {
		digest = workercompletion.AppendWorkerDecisionDigest(digest, *decisionReq)
	}
	env := WorkerCompletionEnvelope{
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
		} else if OverlayAwaitingPromote(ctx, draft.task) {
			env.MergeStatus = string(api.WorkerMergeStatusPending)
		}
	}
	delivery, err := m.evaluateWorkerDelivery(ctx, env)
	if err != nil {
		return "", err
	}
	env = delivery.envelope
	draft.status, digest = env.State, env.Digest
	if delivery.blocked {
		draft.input.GroundingOut = nil
		draft.childMessages = nil
	}
	envelope := FormatWorkerCompletionEnvelope(env)
	if strings.TrimSpace(draft.input.JobID) != "" && strings.TrimSpace(digest) != "" {
		m.workerDigests.Store(draft.input.JobID, digest)
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
	if err := m.ProjectWorkerCard(ctx, parentID, draft.input.JobID, ws); err != nil {
		return "", err
	}
	if err := MergeWorkerUntrustedIntoParent(ctx, m.store, parentID, draft.input.ChildSessionID); err != nil {
		return "", fmt.Errorf("merge worker untrusted-content state: %w", err)
	}
	if err := MergeWorkerSecretExposureIntoParent(ctx, m.store, parentID, draft.input.ChildSessionID); err != nil {
		return "", fmt.Errorf("merge worker secret-exposure state: %w", err)
	}
	// Completed child transcripts remain durable; turn state does not.
	m.DisposeSessionResources(ctx, draft.input.ChildSessionID)
	// Worker events refresh the parent without a session-status edge.
	if m.grounding != nil && draft.input.DelegationID != "" {
		m.grounding.Reset(parentID)
	}
	return draft.status, nil
}

// WorkerSummaryTags returns tagged worker summaries without prose for grounding.
func (m *Manager) WorkerSummaryTags(ctx context.Context, sessionID string) ([]api.WorkerSummaryMeta, error) {
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

// CitationGroundingRetries returns in-session evidence-grounding retry budget.
func (m *Manager) CitationGroundingRetries() int {
	maxRetries := compaction.DefaultCompactionConfig().MaxCitationGroundingRetries
	if m != nil && m.compactor != nil {
		if n := m.compactor.Config().MaxCitationGroundingRetries; n > 0 {
			maxRetries = n
		}
	}
	return maxRetries
}

// WorkerGroundingRetries returns in-session evidence-grounding retry budget for workers.
func (m *Manager) WorkerGroundingRetries() int {
	maxRetries := compaction.DefaultCompactionConfig().MaxWorkerGroundingRetries
	if m != nil && m.compactor != nil {
		if n := m.compactor.Config().MaxWorkerGroundingRetries; n > 0 {
			maxRetries = n
		}
	}
	if maxRetries <= 0 {
		maxRetries = limits.DefaultWorkerGroundingRetries
	}
	return maxRetries
}

// WorkerSummaryFinalizeOpts returns host limits for worker survey bounding after child runs.
func (m *Manager) WorkerSummaryFinalizeOpts(ctx context.Context, sess *api.Session) workercloseout.WorkerSummaryFinalizeOpts {
	if m == nil {
		return workercloseout.WorkerSummaryFinalizeOpts{}
	}
	maxChars := m.liveCompactionConfigFor(ctx, sess).MaxWorkerSummaryChars
	if maxChars <= 0 {
		maxChars = compaction.DefaultCompactionConfig().MaxWorkerSummaryChars
	}
	return workercloseout.WorkerSummaryFinalizeOpts{
		MaxChars:            maxChars,
		MaxGroundingRetries: m.WorkerGroundingRetries(),
		WorkflowHints:       m.workflowHints,
		RenderWorkerKick:    m.renderWorkerKick,
		WorkspaceCheck:      m.workspaceCheck,
		Ledger:              m.store,
		Pipeline:            m.oarPipeline,
		DecisionPending:     m.workerDecisionPending,
	}
}

// workerDecisionPending reports whether the child is parked on an unanswered
// request_decision.
func (m *Manager) workerDecisionPending(ctx context.Context, childSessionID string) bool {
	if m == nil || m.decisions == nil || strings.TrimSpace(childSessionID) == "" {
		return false
	}
	_, ok, err := m.decisions.Get(ctx, childSessionID)
	return err == nil && ok
}

func (m *Manager) renderWorkerKick(ctx context.Context, kickID string, data map[string]any) (string, error) {
	if m == nil || m.prompts == nil {
		return "", nil
	}
	// kickID is the Binding.render stem queued by Emit / EmitMatch.
	return m.prompts.RenderKick(ctx, strings.TrimSpace(kickID), data)
}
