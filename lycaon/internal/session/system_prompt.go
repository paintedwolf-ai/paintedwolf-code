package session

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetPromptEngine wires system prompt template rendering for LLM completion.
func (m *Manager) SetPromptEngine(engine prompts.PromptTemplateEngine) {
	if m != nil {
		m.prompts = engine
		m.Closeout.SetPrompts(engine)
		m.Workers.SetPromptEngine(engine)
		m.Nudges.SetPrompts(engine)
		m.PolicyIndex.SetRenderer(engine)
		guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
		m.ensureCoordinatorRuntime().Kicks().SetPromptEngine(engine)
	}
}

// SetCoordinatorTurnFrameSource wires coordinator workflow state.
func (m *Manager) SetCoordinatorTurnFrameSource(source inject.CoordinatorTurnFrameSource) {
	if m != nil {
		m.coordinatorFrame = source
		m.Guards.SetFrame(source)
		m.Runner.PostTurn.SetFrame(source)
		m.Runner.Preparation.SetFrame(source)
		m.Guidance.SetFrame(source)
	}
}

// SetWorkerContextBuilder wires host-built worker leg context for child Prompt prepend.
func (m *Manager) SetWorkerContextBuilder(b assembly.WorkerContextBuilder) {
	if m != nil {
		m.workerContext = b
		m.Workers.SetContext(b)
	}
}

// SetWorkflowHints wires the WORKFLOW_* hint registry and gate feedback catalog.
func (m *Manager) SetWorkflowHints(cfg *guidance.HintConfig, gateFeedback *feedback.GateFeedbackCatalog) {
	if m != nil {
		m.workflowHints = cfg
		m.Workers.Summaries.SetEvaluation(m.workspaceCheck, m.workflowHints, m.ToolPolicy.Pipeline)
		m.Workers.SetEvaluation(m.workspaceCheck, m.workflowHints, m.ToolPolicy.Pipeline)
		m.gateFeedback = gateFeedback
		m.Guidance.SetFeedback(gateFeedback)
		// Phase formatting retains copy evaluated at the rejection occurrence.
		m.toolRejectFormatter = guidance.NewToolRejectFormatter(guidance.NewFeedbackDeduper())
		m.Guards.SetRejects(m.rejectFmt, m.toolRejectFormatter)
		m.toolOutputEnricher = guidance.NewToolOutputEnricher(cfg, gateFeedback)
	}
}

// SetWorkspaceChecker wires git/workspace proof for worker summary validation.
func (m *Manager) SetWorkspaceChecker(c workercompletion.WorkspaceChangeChecker) {
	if m != nil {
		m.workspaceCheck = c
		m.Guards.SetWorkspaceCheck(c)
		m.Workers.Summaries.SetEvaluation(m.workspaceCheck, m.workflowHints, m.ToolPolicy.Pipeline)
		m.Workers.SetEvaluation(m.workspaceCheck, m.workflowHints, m.ToolPolicy.Pipeline)
	}
}

func (m *Manager) buildCompletionMessages(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	frame *inject.CoordinatorTurnFrame,
) ([]api.Message, error) {
	return m.ensureCoordinatorRuntime().BuildCompletionMessages(ctx, sess, history, frame)
}

// CoordinatorRunContext returns the same block exposed on Prompt prepend (optional GET).
func (m *Manager) CoordinatorRunContext(ctx context.Context, sessionID string) (api.CoordinatorRunContext, error) {
	if m == nil || m.store == nil {
		return api.CoordinatorRunContext{}, fmt.Errorf("session store not configured")
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return api.CoordinatorRunContext{}, err
	}
	if m.coordinatorFrame == nil {
		return api.CoordinatorRunContext{}, nil
	}
	frame, err := m.coordinatorFrame.BuildCoordinatorTurnFrame(ctx, sessionID, sess)
	if err != nil {
		return api.CoordinatorRunContext{}, err
	}
	runCtx := frame.RunContext
	state := m.Workers.State.ForSession(ctx, sess)
	if wirePhase := batch.ToWirePhase(state.BatchPhase); wirePhase != "" {
		runCtx.BatchPhase = wirePhase
		runCtx.BatchSeq = state.BatchSeq
	}
	return runCtx, nil
}
