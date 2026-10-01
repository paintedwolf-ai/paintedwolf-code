package contract

import (
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// ContextDietMatrixRow is one coordinator tripartite budget/diet fixture.
type ContextDietMatrixRow struct {
	Name       string
	RunCtx     api.CoordinatorRunContext
	Sess       *api.Session
	History    []api.Message
	UserPrompt string
	SurfaceID  string
	ModeRefs   []string
	MaxModes   int
	State      surface.ImplementSessionState
}

// batchReadySessionState is a wrapup-admitted idle snapshot for tripartite fixtures.
func batchReadySessionState() surface.ImplementSessionState {
	return surface.WithWrapupGates(surface.ImplementSessionState{
		WorkersInFlight:   0,
		PendingOverlayIDs: []string{},
	}, true, false)
}

// ContextDietMatrix is the SSOT for coordinator_tripartite prompt budget fixtures.
var ContextDietMatrix = []ContextDietMatrixRow{
	{
		Name:       "implement_investigate_first_user",
		UserPrompt: "Research this repo",
		SurfaceID:  tools.SurfaceImplementInvestigate,
		ModeRefs:   []string{"implement-investigate"},
		MaxModes:   2,
		// First-user investigate is the plan-authoring window where both verify steers can
		// fire (undeclared command + unverified work on an inline edit) — measure that
		// worst-case render so the cap covers both clauses.
		State: surface.ImplementSessionState{VerifyUnverified: true},
	},
	{
		Name:       "implement_synthesis_path_explorer_chain",
		History:    WorkerCompletionHistory(orchestration.ProfilePathExplorer),
		UserPrompt: surface.HostLoopWakeSentinel,
		SurfaceID:  "implement_synthesis",
		ModeRefs:   []string{"implement-synthesis"},
		MaxModes:   2,
		State:      batchReadySessionState(),
	},
	{
		Name:       "implement_synthesis_repo_researcher_chain",
		History:    WorkerCompletionHistory(orchestration.ProfileRepoResearcher),
		UserPrompt: surface.HostLoopWakeSentinel,
		SurfaceID:  "implement_synthesis",
		ModeRefs:   []string{"implement-synthesis"},
		MaxModes:   2,
		State:      batchReadySessionState(),
	},
	{
		Name:       "implement_synthesis_implementer_chain",
		History:    WorkerCompletionHistory(orchestration.ProfileImplementer),
		UserPrompt: surface.HostLoopWakeSentinel,
		SurfaceID:  "implement_synthesis",
		ModeRefs:   []string{"implement-synthesis"},
		MaxModes:   2,
		State:      batchReadySessionState(),
	},
	{
		Name:       "implement_overlay_promote_pending_writes",
		History:    WorkerCompletionHistory(orchestration.ProfileImplementer),
		UserPrompt: surface.HostLoopWakeSentinel,
		SurfaceID:  surface.SurfaceImplementOverlayPromote,
		ModeRefs:   []string{"implement-overlay-promote"},
		MaxModes:   2,
		State:      surface.ImplementSessionState{PendingOverlayIDs: []string{"job-1"}, VerifyUnverified: true},
	},
	{
		Name:       "implement_investigate_follow_up",
		History:    []api.Message{{Role: api.MessageRoleUser, Content: "Hi"}, {Role: api.MessageRoleAssistant, Content: "Hello"}},
		UserPrompt: "Explain auth",
		SurfaceID:  tools.SurfaceImplementInvestigate,
		ModeRefs:   []string{"implement-investigate"},
		MaxModes:   2,
	},
	{
		Name: "implement_dispatch_workflow_orchestrate",
		RunCtx: api.CoordinatorRunContext{
			WorkflowDefaultExecutionMode: surface.ExecutionModeFamilyOrchestrate,
		},
		Sess:       &api.Session{Posture: api.SessionPostureBuild},
		UserPrompt: "Summarize findings",
		SurfaceID:  surface.SurfaceImplementDispatch,
		ModeRefs:   []string{"implement-dispatch"},
		MaxModes:   2,
		// Dispatch shares the verify-before-close partial; an unverified prior batch fires
		// the self-check steer here too — measure it so the at-cap dispatch budget covers it.
		State: surface.ImplementSessionState{VerifyUnverified: true},
	},
	{
		Name: "implement_synthesis_with_feedback_overlay",
		RunCtx: api.CoordinatorRunContext{
			WorkflowID: "plan", CurrentPhase: "intake", SurfaceProfile: "plan",
			PendingFeedback: &api.PendingFeedback{PhaseID: "intake", Prompt: "choose"},
		},
		Sess:       &api.Session{Posture: api.SessionPostureSpec},
		UserPrompt: surface.HostLoopWakeSentinel,
		SurfaceID:  "await_user",
		ModeRefs:   []string{"await-user-input", "feedback"},
		MaxModes:   2,
	},
	{
		Name:       "workflow_compose",
		RunCtx:     api.CoordinatorRunContext{HasComposeDraft: true},
		UserPrompt: surface.HostLoopWakeSentinel,
		SurfaceID:  "workflow_compose",
		ModeRefs:   []string{"compose"},
	},
	{
		Name:       "implement_investigate_idle",
		UserPrompt: "fix the auth bug in src/auth.go",
		SurfaceID:  tools.SurfaceImplementInvestigate,
		ModeRefs:   []string{"implement-investigate"},
		MaxModes:   2,
	},
	{
		Name: "plan_research_surface",
		RunCtx: api.CoordinatorRunContext{
			WorkflowID: "plan", CurrentPhase: "research", SurfaceProfile: "plan",
		},
		Sess:       &api.Session{Posture: api.SessionPostureSpec},
		UserPrompt: surface.HostLoopWakeSentinel,
		SurfaceID:  "plan_research",
		ModeRefs:   []string{"plan-research"},
	},
}

// WorkerCompletionHistory builds a minimal worker completion transcript for tripartite fixtures.
func WorkerCompletionHistory(agentType string) []api.Message {
	return []api.Message{{
		Role:    api.MessageRoleAssistant,
		Content: `<task job_id="job-contract" agent_type="` + agentType + `" state="complete"><summary>ok</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:  "job-contract",
			AgentType: agentType,
			Status:    "complete",
		},
	}}
}

func coordinatorTurnHistory(history []api.Message, prompt string) []api.Message {
	out := append([]api.Message(nil), history...)
	if prompt == "" {
		return out
	}
	msg := api.Message{
		Role:       api.MessageRoleUser,
		Origin:     api.MessageOriginUser,
		Visibility: api.MessageVisibilityTranscript,
		Content:    prompt,
	}
	if prompt == surface.HostLoopWakeSentinel {
		msg.Origin = api.MessageOriginHost
		msg.Visibility = api.MessageVisibilityInternal
		msg.Kind = api.MessageKindHostLoopWake
	}
	return append(out, msg)
}
