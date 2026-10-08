package assembly

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const defaultCoordinatorPromptTemplate = "agents/coordinator-core.md"

// AgentProfileResolver resolves agent profiles for prompt template selection.
type AgentProfileResolver interface {
	Get(id string) (agentdef.Profile, error)
}

// ActiveWorkflowManifest holds runtime fields from the active workflow manifest.
type ActiveWorkflowManifest struct {
	CoordinatorProfile string
}

// WorkflowManifestSource supplies active workflow manifest fields for assembly.
type WorkflowManifestSource interface {
	ActiveManifest(ctx context.Context, sessionID string) (ActiveWorkflowManifest, bool)
}

// ScanGuidanceHook prepends ephemeral scan guidance before LLM completion.
type ScanGuidanceHook interface {
	PrependGuidance(ctx context.Context, sessionID string, messages []api.Message) []api.Message
}

type BoardPrependHook interface {
	PrependBoardIfChanged(ctx context.Context, sess *api.Session, run api.CoordinatorRunContext) (block string, ok bool)
	WorkerBoard(ctx context.Context, sess *api.Session) (block string, ok bool)
	BoardInjectHash(sessionID string) string
	InvalidateOrientation(sessionID string)
}

// PromptToolLister returns prompt-visible tool metas for this session turn.
type PromptToolLister func(ctx context.Context, sess *api.Session, profileID string) ([]tools.ToolMeta, error)

// BoardOrientReadyRecorder marks recon_or_board_ready after pack-board inject.
type BoardOrientReadyRecorder interface {
	RecordBoardOrientReady(ctx context.Context, sessionID, injectKey string) error
}

// WorkspaceRootsLoader supplies prompt workspace root rows for a session.
type WorkspaceRootsLoader func(ctx context.Context, sess *api.Session) (roots []map[string]any, rootCount int, activePath string)

// ProjectOverlayRootPaths resolves ordered overlay root paths for prompt/rules warm.
type ProjectOverlayRootPaths func(ctx context.Context, sess *api.Session) []string

// SessionView resolves the catalog-derived registries for a session turn.
// When wired its answer is authoritative; unwired means device (Active) loaders.
type SessionView func(ctx context.Context, sess *api.Session) *catalogview.View

// AgentsMDIndexInjector renders the cached session AGENTS.md index for each request.
type AgentsMDIndexInjector func(ctx context.Context, sess *api.Session) (block api.Message, ok bool)

// AgentsMDChainInjector renders path-scoped AGENTS.md chain content for a turn.
type AgentsMDChainInjector func(ctx context.Context, sess *api.Session, relPath string) (block api.Message, err error)

// AssemblyDeps wires prompt message assembly for coordinator and worker turns.
type AssemblyDeps struct {
	Prompts                 prompts.PromptTemplateEngine
	Injects                 *prompts.InjectRenderer
	WorkspaceRoots          WorkspaceRootsLoader
	ProjectOverlayRootPaths ProjectOverlayRootPaths
	SessionView             SessionView
	AgentsMDIndex           AgentsMDIndexInjector
	AgentsMDChain           AgentsMDChainInjector
	Limits                  func(context.Context, *api.Session) settings.SessionLimits
	Agents                  AgentProfileResolver
	Workflows               WorkflowManifestSource
	CoordinatorFrame        inject.CoordinatorTurnFrameSource
	WorkerContext           WorkerContextBuilder
	SiblingNoteDelivery     SiblingNoteDeliveryRecorder
	PeerReservations        PeerReservationSource
	WorkflowHints           *guidance.HintConfig
	GateFeedback            *feedback.GateFeedbackCatalog
	ScanGuidance            ScanGuidanceHook
	// RepoKnownEmpty reports a measured empty workspace. An unmeasured tree is
	// unknown, not empty.
	RepoKnownEmpty   func(ctx context.Context, workspacePath string) bool
	Board            BoardPrependHook
	BoardOrientReady BoardOrientReadyRecorder
	EnrichHistory    func(sessionID string, history []api.Message) []api.Message
	PromptToolLister PromptToolLister
	// LoadedTools returns the tools the turn ledger loaded beyond the surface floor.
	LoadedTools func(sessionID string) map[string]bool
	// OmittedUnits returns the instruction units the turn ledger left out.
	OmittedUnits           func(sessionID string) map[string]bool
	SkillPreload           func(sessionID string) *turnload.SkillPreload
	SkillPointer           func(sessionID string) *turnload.SkillRank
	ModelVision            func(context.Context, *api.Session) bool
	ImplementSessionState  func(ctx context.Context, sess *api.Session) surface.ImplementSessionState
	LoadExecutionModeState func(ctx context.Context, sessionID string) surface.ExecutionModeState
	SaveExecutionModeState func(ctx context.Context, sessionID, family string)
	WebSearchEnabled       func() bool
	SynthesisEvidence      SynthesisEvidenceSource
	CommandJobs            func(sessionID string) []bgprocess.JobSnapshot
	// HeldCalls lists the session's tool calls held past their foreground wait.
	HeldCalls func(sessionID string) []heldcall.Running
	// TurnSourceBriefs returns each turn's source-change brief, keyed by the
	// message that opened the turn.
	TurnSourceBriefs func(ctx context.Context, sess *api.Session) map[string]inject.SourceChangeBrief
	// EffectivePromptSurface supplies one project-gated skill/environment view.
	EffectivePromptSurface func(ctx context.Context, sess *api.Session) prompts.AgentPromptSurface
}

// SynthesisEvidenceSource supplies evidence_digest blocks for wrapup and adjudication.
type SynthesisEvidenceSource interface {
	SynthesisEvidenceForAssembly(ctx context.Context, sess *api.Session, surfaceID string) string
}

// AssemblyEngine builds LLM completion messages with persona, tripartite policy, and run context.
type AssemblyEngine struct {
	// depsAtomic stores immutable wiring snapshots for concurrent turns.
	depsAtomic atomic.Pointer[AssemblyDeps]
	cache      SessionPromptCache
}

func (e *AssemblyEngine) SetDeps(deps AssemblyDeps) {
	if e == nil {
		return
	}
	e.depsAtomic.Store(&deps)
}

// deps returns the current immutable wiring snapshot.
func (e *AssemblyEngine) deps() AssemblyDeps {
	if e == nil {
		return AssemblyDeps{}
	}
	if p := e.depsAtomic.Load(); p != nil {
		return *p
	}
	return AssemblyDeps{}
}

// ResolveSystemPromptTemplate selects the session's system prompt.
func ResolveSystemPromptTemplate(sess *api.Session, agents AgentProfileResolver, coordinatorProfile string) string {
	if sess != nil && strings.TrimSpace(sess.AgentType) != "" && agents != nil {
		if p, err := agents.Get(strings.TrimSpace(sess.AgentType)); err == nil {
			if ref := strings.TrimSpace(p.SystemPromptTemplate); ref != "" {
				return ref
			}
		}
	}
	if cp := strings.TrimSpace(coordinatorProfile); cp != "" && agents != nil {
		if p, err := agents.Get(cp); err == nil {
			if ref := strings.TrimSpace(p.SystemPromptTemplate); ref != "" {
				return ref
			}
		}
	}
	if agents != nil {
		if p, err := agents.Get(orchestration.ProfileCoordinator); err == nil {
			if ref := strings.TrimSpace(p.SystemPromptTemplate); ref != "" {
				return ref
			}
		}
	}
	return defaultCoordinatorPromptTemplate
}

// BuildCompletionMessages assembles the completion prompt.
func (e *AssemblyEngine) BuildCompletionMessages(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	turnFrame *inject.CoordinatorTurnFrame,
) ([]api.Message, error) {
	if e == nil {
		return history, nil
	}
	deps := e.deps()
	prompt := &promptSurface{wiring: deps, cache: &e.cache}
	contextAssembler := &turnContextAssembler{surface: prompt}
	messages := api.FilterPromptHistory(history)
	if deps.Prompts == nil {
		if deps.ScanGuidance != nil {
			messages = deps.ScanGuidance.PrependGuidance(ctx, sess.ID, history)
		}
		return transcript.Project(llm.AppendHostSecretRedactionNotice(messages)), nil
	}

	turn := e.cache.LoadTurn(sess.ID)

	templateRef, err := prompt.resolveSystemPromptRef(ctx, sess)
	if err != nil {
		return nil, err
	}
	vars := map[string]any{"project_dir": sess.WorkspacePath}
	if roots, count, activePath, ok := prompt.workspaceRootsForTurn(ctx, sess, turn); ok {
		vars["root_count"] = count
		vars["workspace_roots"] = roots
		if activePath != "" {
			vars["project_dir"] = activePath
		}
	}

	coordinator := surface.IsCoordinatorSession(sess)
	if coordinator {
		// Coordinator rosters use a top-level heading.
		vars["skills_heading"] = "##"
	}
	prompt.applyToolBudgetVars(ctx, sess, coordinator, vars)
	if deps.ModelVision != nil {
		prompts.MergeModelCapabilityVars(deps.ModelVision(ctx, sess), vars)
	}
	frame := inject.CoordinatorTurnFrame{}
	if turnFrame != nil {
		frame = *turnFrame
	} else if coordinator && deps.CoordinatorFrame != nil {
		frame, err = deps.CoordinatorFrame.BuildCoordinatorTurnFrame(ctx, sess.ID, sess)
		if err != nil {
			return nil, err
		}
	}
	coordinatorSurface := coordinator && (!frame.Machine.Compiled() || frame.Machine.ProfileID == orchestration.ProfileCoordinator)
	boardBlock := ""
	if coordinator && !sess.IsWorkerChild() {
		frame, boardBlock, err = contextAssembler.prepareCoordinatorBoard(ctx, sess, frame, turn)
		if err != nil {
			return nil, err
		}
		// Stamped on the frame so every projection reads one roster.
		frame.Roster = prompt.resolveTurnRoster(ctx, sess, frame, history, turn)
	}
	if turnFrame != nil {
		*turnFrame = frame
	}
	if !coordinatorSurface {
		prompt.mergeVisibleTools(ctx, sess, frame, vars)
		prompt.mergeVisibleToolCapabilityVars(vars)
	}
	prompt.mergeEffectivePromptSurface(ctx, sess, frame, vars)

	var pendingKickIDs []string
	if turn != nil {
		pendingKickIDs = turn.PendingKickIDs
	}

	posture := string(sess.Posture)
	settingsFP := "default"
	if deps.Limits != nil {
		settingsFP = deps.Limits(ctx, sess).SettingsFingerprint()
	}
	if surfaceFP, ok := vars["prompt_surface_fingerprint"].(string); ok && strings.TrimSpace(surfaceFP) != "" {
		settingsFP += ";surface=" + surfaceFP
	}
	pe, promptRevision, err := prompt.projectPromptsSnapshot(ctx, sess)
	if err != nil {
		return nil, err
	}
	var rendered string
	if coordinatorSurface {
		rendered, err = prompt.renderCoordinatorStablePrompt(ctx, pe, promptRevision, sess, frame, history, templateRef, posture, settingsFP, turn, vars)
		if err != nil {
			return nil, err
		}
	} else {
		rendered, err = prompt.renderSystemPromptWithEngine(ctx, pe, sess, templateRef, vars)
		if err != nil {
			return nil, fmt.Errorf("system prompt %q: %w", templateRef, err)
		}
	}

	out := []api.Message{{Role: api.MessageRoleSystem, Content: rendered, ContextPinned: true}}

	// The standing prefix ends after the pre-history injects; its read point
	// survives a rewritten history.
	out, prefixIdx := contextAssembler.appendPreHistorySystemInjects(ctx, sess, turn, out, 0)
	lastStableIdx := prefixIdx

	tailInjects, err := contextAssembler.buildTailSystemInjects(
		ctx, sess, coordinatorSurface, frame, pendingKickIDs, history, turn,
	)
	if err != nil {
		return nil, err
	}

	if deps.EnrichHistory != nil {
		messages = deps.EnrichHistory(sess.ID, messages)
	}
	if coordinator && !sess.IsWorkerChild() {
		messages = contextAssembler.interleaveSourceBriefs(ctx, sess, turn, messages)
	}
	if deps.ScanGuidance != nil {
		out = append(out, deps.ScanGuidance.PrependGuidance(ctx, sess.ID, messages)...)
	} else {
		out = append(out, messages...)
	}
	out = llm.AppendHostSecretRedactionNotice(out)
	// Cached prefixes extend through stable history.
	if len(out) > 0 {
		lastStableIdx = len(out) - 1
	}

	// Dynamic host state follows cached history.
	out = append(out, tailInjects...)

	// Volatile worker context follows cached history.
	if sess.IsWorkerChild() && deps.WorkerContext != nil {
		legMsgs, err := contextAssembler.appendWorkerLegInject(ctx, sess, rendered, turn)
		if err != nil {
			return nil, err
		}
		out = append(out, legMsgs...)
	}

	if boardBlock != "" {
		out = append(out, api.Message{Role: api.MessageRoleSystem, Content: boardBlock})
	}

	markPromptCacheBreakpoints(out, prefixIdx, lastStableIdx)

	turn.Iteration++
	return transcript.Project(deduplicateProjectGuidance(out)), nil
}

// markPromptCacheBreakpoints marks each stable boundary a provider may cache
// up to: the end of the standing prefix and the end of stable history. With
// no history the one boundary closes the standing prefix.
func markPromptCacheBreakpoints(out []api.Message, standingIdx, historyIdx int) {
	mark := func(idx int, tier api.PromptCacheTier) {
		if idx >= 0 && idx < len(out) && out[idx].PromptCacheBreakpoint == api.PromptCacheTierNone {
			out[idx].PromptCacheBreakpoint = tier
		}
	}
	mark(standingIdx, api.PromptCacheTierStanding)
	mark(historyIdx, api.PromptCacheTierHistory)
}

// BeginPromptTurn starts a cached assembly turn for sessionID.
func (e *AssemblyEngine) BeginPromptTurn(sessionID string, pendingKickIDs ...string) {
	if e != nil {
		e.cache.BeginTurn(sessionID, pendingKickIDs...)
	}
}

// EndPromptTurn clears cached assembly state for sessionID.
func (e *AssemblyEngine) EndPromptTurn(sessionID string) {
	if e == nil {
		return
	}
	turn := e.cache.LoadTurn(sessionID)
	if family := strings.TrimSpace(turn.CurrentExecutionModeFamily); family != "" {
		if save := e.deps().SaveExecutionModeState; save != nil {
			save(context.Background(), sessionID, family)
		}
	}
	e.cache.EndTurn(sessionID)
}

// PushModeTransitionCause records an explicit execution-mode entry for the active turn.
func (e *AssemblyEngine) PushModeTransitionCause(sessionID string, cause surface.ModeTransitionCause) {
	if e != nil {
		e.cache.PushModeTransitionCause(sessionID, cause)
	}
}

// SetTurnSurfaceID records the coordinator surface for the active prompt turn.
func (e *AssemblyEngine) SetTurnSurfaceID(sessionID, surfaceID string) {
	if e != nil {
		e.cache.SetTurnSurfaceID(sessionID, surfaceID)
	}
}

// TurnSurfaceID returns the coordinator surface for the active prompt turn.
func (e *AssemblyEngine) TurnSurfaceID(sessionID string) string {
	if e == nil {
		return ""
	}
	return e.cache.TurnSurfaceID(sessionID)
}

// Cache exposes the assembly turn cache (tests).
func (e *AssemblyEngine) Cache() *SessionPromptCache {
	if e == nil {
		return &SessionPromptCache{}
	}
	return &e.cache
}

// CommitWorkerContext advances communication only after a successful response.
func (e *AssemblyEngine) CommitWorkerContext(ctx context.Context, sessionID, responseID string) error {
	recorder := e.deps().SiblingNoteDelivery
	if recorder == nil {
		return nil
	}
	turn := e.cache.LoadTurn(sessionID)
	if len(turn.PendingSiblingNotes) == 0 && turn.LastSeenSiblingNoteID == 0 {
		return nil
	}
	return recorder.CommitSiblingNotes(ctx, sessionID, responseID, turn.LastSeenSiblingNoteID, turn.PendingSiblingNotes)
}
