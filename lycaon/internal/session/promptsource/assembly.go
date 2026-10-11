package promptsource

import (
	"context"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/repoinfo"
	sessioncatalog "github.com/lycaon/lycaon/internal/session/catalog"
	"github.com/lycaon/lycaon/internal/session/closeoutassembly"
	sessionlimits "github.com/lycaon/lycaon/internal/session/limits"
	"github.com/lycaon/lycaon/internal/session/loading"
	"github.com/lycaon/lycaon/internal/session/policyindex"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	"github.com/lycaon/lycaon/internal/session/profiles"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/sourcebrief"
	"github.com/lycaon/lycaon/internal/session/toolpresentation"
	"github.com/lycaon/lycaon/internal/session/turnguards"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/pkg/api"
)

type AssemblyWorkflow struct {
	Manifests   assembly.WorkflowManifestSource
	Orientation assembly.BoardOrientReadyRecorder
}

type Assembly struct {
	Workflow      AssemblyWorkflow
	Briefs        *sourcebrief.Service
	Catalog       *sessioncatalog.Service
	Closeout      *closeoutassembly.Service
	Feedback      *feedback.GateFeedbackCatalog
	Frame         inject.CoordinatorTurnFrameSource
	Guards        *turnguards.Service
	Hints         *guidance.HintConfig
	Limits        *sessionlimits.Service
	Loading       *loading.Service
	Model         *Model
	Notes         *workeroutcomes.Notes
	PolicyIndex   *policyindex.Service
	Processes     *processcontrol.Service
	Profiles      *profiles.Service
	Prompts       prompts.PromptTemplateEngine
	Repository    repoinfo.Provider
	Runtime       *coordinator.Runtime
	Scan          assembly.ScanGuidanceHook
	Stash         *toolpresentation.Stash
	State         *workeroutcomes.State
	WebResearch   *webresearch.ConfigStore
	WorkerContext assembly.WorkerContextBuilder
	Workspace     *sessionscope.Service
	Workspaces    *workerworkspace.Service
}

func (m *Assembly) Build() assembly.AssemblyDeps {
	rt := m.Runtime
	return assembly.AssemblyDeps{
		Prompts:               m.Prompts,
		Injects:               prompts.NewInjectRenderer(m.Prompts),
		Limits:                m.Limits.Effective,
		Agents:                m.Profiles.Agents,
		Workflows:             m.Workflow.Manifests,
		CoordinatorFrame:      m.Frame,
		WorkerContext:         m.WorkerContext,
		SiblingNoteDelivery:   m.Notes,
		PeerReservations:      m.Workspaces,
		ImplementSessionState: m.State.ForSession,
		LoadExecutionModeState: func(_ context.Context, sessionID string) surface.ExecutionModeState {
			return rt.ExecutionModeStore().Load(sessionID)
		},
		SaveExecutionModeState: func(_ context.Context, sessionID, family string) {
			rt.ExecutionModeStore().Save(sessionID, family)
		},
		WorkflowHints: m.Hints,
		GateFeedback:  m.Feedback,
		ScanGuidance:  m.Scan,
		RepoKnownEmpty: func(ctx context.Context, workspacePath string) bool {
			return repoinfo.MeasuredEmpty(ctx, m.Repository, workspacePath)
		},
		Board:            rt.Board(),
		BoardOrientReady: m.Workflow.Orientation,
		EnrichHistory: func(sessionID string, history []api.Message) []api.Message {
			if m == nil || m.Stash == nil {
				return history
			}
			return toolpresentation.EnrichHistory(sessionID, history, m.Stash)
		},
		PromptToolLister:        m.PromptTools,
		LoadedTools:             m.Loading.LoadedTools,
		OmittedUnits:            m.Loading.OmittedUnits,
		SkillPreload:            m.Loading.SkillPreload,
		SkillPointer:            m.Loading.SkillPointer,
		WorkspaceRoots:          m.Workspace.PromptRootRows,
		ProjectOverlayRootPaths: m.Workspace.PromptRoots,
		SessionView:             m.Catalog.ViewForSession,
		AgentsMDIndex:           m.PolicyIndex.Index,
		AgentsMDChain:           m.PolicyIndex.Chain,
		WebSearchEnabled:        m.WebSearchEnabled,
		SynthesisEvidence:       m.Closeout,
		CommandJobs: func(sessionID string) []bgprocess.JobSnapshot {
			if m == nil || m.Processes.Background == nil {
				return nil
			}
			return m.Processes.Background.ActiveJobs(sessionID)
		},
		HeldCalls: func(sessionID string) []heldcall.Running {
			if m == nil {
				return nil
			}
			return m.Processes.Held.Ledger(sessionID)
		},
		TurnSourceBriefs:       m.Briefs.Recorded,
		ModelVision:            m.Model.Vision,
		EffectivePromptSurface: m.PromptSurface,
	}
}
func (m *Assembly) PromptSurface(ctx context.Context, sess *api.Session) prompts.AgentPromptSurface {
	if m == nil || sess == nil {
		return prompts.AgentPromptSurface{}
	}
	profileID, _ := m.Profiles.PromptToolProfile(ctx, sess)
	return m.Profiles.CompileMachine(ctx, sess, profileID).Surface
}
func (m *Assembly) PromptTools(ctx context.Context, sess *api.Session, profileID string) ([]tools.ToolMeta, error) {
	if m == nil || sess == nil {
		return nil, nil
	}
	policy := m.Guards.Policy()
	if policy == nil {
		return nil, nil
	}
	metas := policy.ListForPrompt(ctx, sess, profileID)
	machine := m.Profiles.CompileMachine(ctx, sess, profileID)
	return tools.HideSkillsReadWhenEmpty(metas, machine.SkillCount), nil
}

func (m *Assembly) WebSearchEnabled() bool {
	return m.WebResearch == nil || m.WebResearch.SearchEnabled()
}
