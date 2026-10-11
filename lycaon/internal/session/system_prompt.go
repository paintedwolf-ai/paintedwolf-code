package session

import (
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
)

// SetPromptEngine wires system prompt template rendering for LLM completion.
func (m *Host) SetPromptEngine(engine prompts.PromptTemplateEngine) {
	if m != nil {
		m.Coordinator.Context.Prompts = engine
		m.Coordinator.Assembly.Prompts = engine

		m.Coordinator.Closeout.SetPrompts(engine)
		m.Workers.SetPromptEngine(engine)
		m.Coordinator.Nudges.SetPrompts(engine)
		m.Coordinator.PolicyIndex.SetRenderer(engine)
		guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
		m.Coordinator.Runtime.Kicks().SetPromptEngine(engine)
	}
}

// SetCoordinatorTurnFrameSource wires coordinator workflow state.
func (m *Host) SetCoordinatorTurnFrameSource(source inject.CoordinatorTurnFrameSource) {
	if m != nil {
		m.Coordinator.Context.Frame = source
		m.Coordinator.Tools.Frame = source
		m.Coordinator.Assembly.Frame = source
		m.Coordinator.Loop.Frame = source

		m.Coordinator.Context.Frame = source
		m.Coordinator.Guards.SetFrame(source)
		m.Runner.PostTurn.SetFrame(source)
		m.Runner.Preparation.SetFrame(source)
		m.Coordinator.Guidance.SetFrame(source)
	}
}

// SetWorkerContextBuilder wires host-built worker leg context for child Prompt prepend.
func (m *Host) SetWorkerContextBuilder(b assembly.WorkerContextBuilder) {
	if m != nil {
		m.Coordinator.Assembly.WorkerContext = b

		m.Workers.SetContext(b)
	}
}

// SetWorkflowHints wires the WORKFLOW_* hint registry and gate feedback catalog.
func (m *Host) SetWorkflowHints(cfg *guidance.HintConfig, gateFeedback *feedback.GateFeedbackCatalog) {
	if m != nil {
		m.Coordinator.Completion.Hints = cfg
		m.Coordinator.Assembly.Hints = cfg

		m.Workers.Summaries.SetEvaluation(m.Workers.WorkspaceCheck, m.Coordinator.Completion.Hints, m.ToolPolicy.Pipeline)
		m.Workers.SetEvaluation(m.Workers.WorkspaceCheck, m.Coordinator.Completion.Hints, m.ToolPolicy.Pipeline)
		m.Coordinator.Assembly.Feedback = gateFeedback

		m.Coordinator.Guidance.SetFeedback(gateFeedback)
		// Phase formatting retains copy evaluated at the rejection occurrence.
		m.Coordinator.Feedback.Rejects = guidance.NewToolRejectFormatter(guidance.NewFeedbackDeduper())
		m.Resources.Tools.Rejects = m.Coordinator.Feedback.Rejects
		m.Coordinator.Guards.SetRejects(m.Coordinator.Completion.Rejects, m.Coordinator.Feedback.Rejects)
		m.Coordinator.Tools.Enricher = guidance.NewToolOutputEnricher(cfg, gateFeedback)

		m.Resources.Tools.Outputs = m.Coordinator.Tools.Enricher
	}
}

// SetWorkspaceChecker wires git/workspace proof for worker summary validation.
func (m *Host) SetWorkspaceChecker(c workercompletion.WorkspaceChangeChecker) {
	if m != nil {
		m.Workers.WorkspaceCheck = c
		m.Coordinator.Guards.SetWorkspaceCheck(c)
		m.Workers.Summaries.SetEvaluation(m.Workers.WorkspaceCheck, m.Coordinator.Completion.Hints, m.ToolPolicy.Pipeline)
		m.Workers.SetEvaluation(m.Workers.WorkspaceCheck, m.Coordinator.Completion.Hints, m.ToolPolicy.Pipeline)
	}
}

// CoordinatorRunContext returns the same block exposed on Prompt prepend (optional GET).
