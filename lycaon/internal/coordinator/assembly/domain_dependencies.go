package assembly

import (
	"context"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

type promptSurfaceDeps struct {
	Agents                  AgentProfileResolver
	EffectivePromptSurface  func(ctx context.Context, sess *api.Session) prompts.AgentPromptSurface
	ImplementSessionState   func(ctx context.Context, sess *api.Session) surface.ImplementSessionState
	Limits                  func(context.Context, *api.Session) settings.SessionLimits
	LoadExecutionModeState  func(ctx context.Context, sessionID string) surface.ExecutionModeState
	LoadedTools             func(sessionID string) map[string]bool
	OmittedUnits            func(sessionID string) map[string]bool
	ProjectOverlayRootPaths ProjectOverlayRootPaths
	PromptToolLister        PromptToolLister
	Prompts                 prompts.PromptTemplateEngine
	RepoKnownEmpty          func(ctx context.Context, workspacePath string) bool
	SessionView             SessionView
	WebSearchEnabled        func() bool
	WorkspaceRoots          WorkspaceRootsLoader
	CoordinatorProfile      func(context.Context, string) string
}

type turnContextDeps struct {
	AgentsMDChain       AgentsMDChainInjector
	AgentsMDIndex       AgentsMDIndexInjector
	Board               BoardPrependHook
	BoardOrientReady    BoardOrientReadyRecorder
	CommandJobs         func(sessionID string) []bgprocess.JobSnapshot
	CoordinatorFrame    inject.CoordinatorTurnFrameSource
	GateFeedback        *feedback.GateFeedbackCatalog
	HeldCalls           func(sessionID string) []heldcall.Running
	Injects             *prompts.InjectRenderer
	PeerReservations    PeerReservationSource
	SiblingNoteDelivery SiblingNoteDeliveryRecorder
	SkillPointer        func(sessionID string) *turnload.SkillRank
	SkillPreload        func(sessionID string) *turnload.SkillPreload
	SynthesisEvidence   SynthesisEvidenceSource
	TurnSourceBriefs    func(ctx context.Context, sess *api.Session) map[string]inject.SourceChangeBrief
	WorkerContext       WorkerContextBuilder
	WorkflowHints       *guidance.HintConfig
	PromptConfigured    bool
}

type promptProjection interface {
	loadExecutionModeState(context.Context, string) surface.ExecutionModeState
	loadedTools(*api.Session) map[string]bool
	resolveCoordinatorProfile(context.Context, *api.Session, inject.CoordinatorTurnFrame, []api.Message, *TurnAssemblyScratch) (surface.TurnProfile, surface.ImplementSessionState)
	resolveTurnRoster(context.Context, *api.Session, inject.CoordinatorTurnFrame, []api.Message, *TurnAssemblyScratch) *inject.AgentRoster
	sessionCatalogView(context.Context, *api.Session) *catalogview.View
	taskOffered(*api.Session, string, int) bool
	workspaceRootsForTurn(context.Context, *api.Session, *TurnAssemblyScratch) ([]map[string]any, int, string, bool)
}

func newAssemblyDomains(snapshot AssemblyDeps, cache *SessionPromptCache) (*promptSurface, *turnContextAssembler) {
	surfaceDeps := promptSurfaceDeps{
		Agents:                  snapshot.Agents,
		EffectivePromptSurface:  snapshot.EffectivePromptSurface,
		ImplementSessionState:   snapshot.ImplementSessionState,
		Limits:                  snapshot.Limits,
		LoadExecutionModeState:  snapshot.LoadExecutionModeState,
		LoadedTools:             snapshot.LoadedTools,
		OmittedUnits:            snapshot.OmittedUnits,
		ProjectOverlayRootPaths: snapshot.ProjectOverlayRootPaths,
		PromptToolLister:        snapshot.PromptToolLister,
		Prompts:                 snapshot.Prompts,
		RepoKnownEmpty:          snapshot.RepoKnownEmpty,
		SessionView:             snapshot.SessionView,
		WebSearchEnabled:        snapshot.WebSearchEnabled,
		WorkspaceRoots:          snapshot.WorkspaceRoots,
	}
	if workflows := snapshot.Workflows; workflows != nil {
		surfaceDeps.CoordinatorProfile = func(ctx context.Context, sessionID string) string {
			manifest, ok := workflows.ActiveManifest(ctx, sessionID)
			if !ok {
				return ""
			}
			return manifest.CoordinatorProfile
		}
	}
	prompt := &promptSurface{deps: surfaceDeps, cache: cache}
	turn := &turnContextAssembler{surface: prompt, deps: turnContextDeps{
		AgentsMDChain:       snapshot.AgentsMDChain,
		AgentsMDIndex:       snapshot.AgentsMDIndex,
		Board:               snapshot.Board,
		BoardOrientReady:    snapshot.BoardOrientReady,
		CommandJobs:         snapshot.CommandJobs,
		CoordinatorFrame:    snapshot.CoordinatorFrame,
		GateFeedback:        snapshot.GateFeedback,
		HeldCalls:           snapshot.HeldCalls,
		Injects:             snapshot.Injects,
		PeerReservations:    snapshot.PeerReservations,
		SiblingNoteDelivery: snapshot.SiblingNoteDelivery,
		SkillPointer:        snapshot.SkillPointer,
		SkillPreload:        snapshot.SkillPreload,
		SynthesisEvidence:   snapshot.SynthesisEvidence,
		TurnSourceBriefs:    snapshot.TurnSourceBriefs,
		WorkerContext:       snapshot.WorkerContext,
		WorkflowHints:       snapshot.WorkflowHints,
		PromptConfigured:    snapshot.Prompts != nil,
	}}
	return prompt, turn
}
