package assembly

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
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

// Missing host measurements leave workspace contents unknown.
func (e *AssemblyEngine) repoKnownEmpty(ctx context.Context, workspacePath string) bool {
	fn := e.deps().RepoKnownEmpty
	return fn != nil && fn(ctx, workspacePath)
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

func (e *AssemblyEngine) resolveSystemPromptRef(ctx context.Context, sess *api.Session) (string, error) {
	coordinatorProfile := ""
	if e != nil && e.deps().Workflows != nil {
		if manifest, ok := e.deps().Workflows.ActiveManifest(ctx, sess.ID); ok {
			coordinatorProfile = manifest.CoordinatorProfile
		}
	}
	return ResolveSystemPromptTemplate(sess, e.agentsForSession(ctx, sess), coordinatorProfile), nil
}

func (e *AssemblyEngine) agentsForSession(ctx context.Context, sess *api.Session) AgentProfileResolver {
	if view := e.sessionCatalogView(ctx, sess); view != nil {
		return view
	}
	return e.deps().Agents
}

// projectPrompts returns the session-scoped prompt engine.
func (e *AssemblyEngine) projectPrompts(ctx context.Context, sess *api.Session) prompts.PromptTemplateEngine {
	pe := e.deps().Prompts
	fe, ok := pe.(*prompts.FileTemplateEngine)
	if !ok || fe == nil || sess == nil {
		return pe
	}
	// Empty trusted roots disable project prompt layers.
	if resolve := e.deps().ProjectOverlayRootPaths; resolve != nil {
		paths := resolve(ctx, sess)
		if len(paths) == 0 {
			return e.attachSessionCatalog(ctx, sess, fe)
		}
		return e.attachSessionCatalog(ctx, sess, fe.WithProjectOverlays(paths))
	}
	return e.attachSessionCatalog(ctx, sess, fe.WithProjectOverlay(sess.WorkspacePath))
}

// projectPromptsSnapshot binds one immutable prompt source for the whole turn.
func (e *AssemblyEngine) projectPromptsSnapshot(ctx context.Context, sess *api.Session) (prompts.PromptTemplateEngine, string, error) {
	pe := e.projectPrompts(ctx, sess)
	if pe == nil {
		return nil, "", nil
	}
	fe, ok := pe.(*prompts.FileTemplateEngine)
	if !ok {
		return pe, "", nil
	}
	snapshot, err := fe.Snapshot()
	if err != nil {
		return nil, "", fmt.Errorf("prompt source snapshot: %w", err)
	}
	revision := snapshot.Revision()
	return snapshot, revision, nil
}

func (e *AssemblyEngine) attachSessionCatalog(ctx context.Context, sess *api.Session, fe *prompts.FileTemplateEngine) *prompts.FileTemplateEngine {
	if e == nil || fe == nil {
		return fe
	}
	if resolve := e.deps().SessionView; resolve != nil {
		if view := resolve(ctx, sess); view != nil && view.Catalog != nil {
			return fe.WithEffectiveCatalog(view.Catalog)
		}
		return fe
	}
	return fe
}

// sessionCatalogView resolves the turn view once (nil when unwired).
func (e *AssemblyEngine) sessionCatalogView(ctx context.Context, sess *api.Session) *catalogview.View {
	if e == nil {
		return nil
	}
	if resolve := e.deps().SessionView; resolve != nil {
		return resolve(ctx, sess)
	}
	return nil
}

func (e *AssemblyEngine) renderSystemPromptWithEngine(ctx context.Context, pe prompts.PromptTemplateEngine, sess *api.Session, templateRef string, vars map[string]any) (string, error) {
	if e == nil || pe == nil {
		return "", errPromptEngineNotConfigured
	}
	agentType := ""
	if sess != nil {
		agentType = strings.TrimSpace(sess.AgentType)
	}
	if fe, ok := pe.(*prompts.FileTemplateEngine); ok && agentType != "" {
		out, err := prompts.RenderPersona(ctx, fe, agentType, vars)
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, prompts.ErrUnknownAgent) {
			return "", err
		}
	}
	return pe.Render(ctx, templateRef, vars)
}

func (e *AssemblyEngine) mergeVisibleTools(ctx context.Context, sess *api.Session, frame inject.CoordinatorTurnFrame, vars map[string]any) {
	if e == nil || vars == nil || sess == nil || e.deps().PromptToolLister == nil {
		return
	}
	profileID := strings.TrimSpace(frame.Machine.ProfileID)
	if agents := e.agentsForSession(ctx, sess); profileID == "" && agents != nil {
		if profile, err := agents.Get(sess.AgentType); err == nil {
			profileID = profile.ToolProfile
		}
	}
	if profileID == "" {
		fe, ok := e.deps().Prompts.(*prompts.FileTemplateEngine)
		if !ok || fe == nil {
			return
		}
		var err error
		profileID, err = prompts.ToolProfileForAgent(sess.AgentType)
		if err != nil {
			return
		}
	}
	metas, err := e.deps().PromptToolLister(ctx, sess, profileID)
	if err != nil || len(metas) == 0 {
		return
	}
	if frame.Machine.Compiled() {
		metas = tools.HideSkillsReadWhenEmpty(metas, frame.Machine.SkillCount)
	}
	names := make([]string, 0, len(metas))
	for _, meta := range metas {
		if strings.TrimSpace(meta.Name) != "" {
			names = append(names, meta.Name)
		}
	}
	vars["visible_tools"] = names
	// A persona renders the units and loaded tools of its own session.
	vars["loaded_tools"] = e.loadedTools(sess)
	vars["omitted_units"] = e.omittedUnits(sess)
}

func (e *AssemblyEngine) mergeEffectivePromptSurface(ctx context.Context, sess *api.Session, frame inject.CoordinatorTurnFrame, vars map[string]any) {
	if e == nil || sess == nil || vars == nil {
		return
	}
	var surface prompts.AgentPromptSurface
	switch {
	case frame.Machine.Compiled():
		surface = frame.Machine.Surface
	case e.deps().EffectivePromptSurface != nil:
		surface = e.deps().EffectivePromptSurface(ctx, sess)
	default:
		return
	}
	for key, value := range prompts.AgentPromptSurfaceTemplateVars(surface) {
		switch value := value.(type) {
		case []map[string]any:
			if len(value) > 0 {
				vars[key] = value
			}
		case string:
			if strings.TrimSpace(value) != "" {
				vars[key] = value
			}
		case int:
			// Preserve the full count when the roster is truncated.
			if value > 0 {
				vars[key] = value
			}
		}
	}
}

func (e *AssemblyEngine) mergeVisibleToolCapabilityVars(vars map[string]any) {
	if vars == nil {
		return
	}
	rootCount := 0
	if v, ok := vars["root_count"].(int); ok {
		rootCount = v
	}
	var visible []string
	switch v := vars["visible_tools"].(type) {
	case []string:
		visible = v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				visible = append(visible, s)
			}
		}
	}
	capability.MergeVars(vars, capability.Derive(rootCount, visible, nil))
}

// workerToolBudget resolves the effective worker tool-budget bounds for a session,
// falling back to host defaults when no limits resolver is wired.
func (e *AssemblyEngine) workerToolBudget(ctx context.Context, sess *api.Session) spawn.WorkerToolBudget {
	if deps := e.deps(); deps.Limits != nil {
		return deps.Limits(ctx, sess).WorkerToolBudget()
	}
	return spawn.DefaultWorkerToolBudget()
}

// applyToolBudgetVars projects role-specific worker budget bounds.
func (e *AssemblyEngine) applyToolBudgetVars(ctx context.Context, sess *api.Session, coordinator bool, vars map[string]any) {
	budget := e.workerToolBudget(ctx, sess)
	if sess.IsWorkerChild() {
		vars["max_tool_loops"] = budget.Effective(sess.MaxToolLoops)
	}
	if coordinator {
		vars["worker_tool_budget_default"] = budget.Default
		vars["worker_tool_budget_min"] = budget.Min
		vars["worker_tool_budget_max"] = budget.Max
	}
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
	messages := api.FilterPromptHistory(history)
	if deps.Prompts == nil {
		if deps.ScanGuidance != nil {
			messages = deps.ScanGuidance.PrependGuidance(ctx, sess.ID, history)
		}
		return transcript.Project(llm.AppendHostSecretRedactionNotice(messages)), nil
	}

	turn := e.cache.LoadTurn(sess.ID)

	templateRef, err := e.resolveSystemPromptRef(ctx, sess)
	if err != nil {
		return nil, err
	}
	vars := map[string]any{"project_dir": sess.WorkspacePath}
	if roots, count, activePath, ok := e.workspaceRootsForTurn(ctx, sess, turn); ok {
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
	e.applyToolBudgetVars(ctx, sess, coordinator, vars)
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
		frame, boardBlock, err = e.prepareCoordinatorBoard(ctx, sess, frame, turn)
		if err != nil {
			return nil, err
		}
		// Stamped on the frame so every projection reads one roster.
		frame.Roster = e.resolveTurnRoster(ctx, sess, frame, history, turn)
	}
	if turnFrame != nil {
		*turnFrame = frame
	}
	if !coordinatorSurface {
		e.mergeVisibleTools(ctx, sess, frame, vars)
		e.mergeVisibleToolCapabilityVars(vars)
	}
	e.mergeEffectivePromptSurface(ctx, sess, frame, vars)

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
	pe, promptRevision, err := e.projectPromptsSnapshot(ctx, sess)
	if err != nil {
		return nil, err
	}
	var rendered string
	if coordinatorSurface {
		rendered, err = e.renderCoordinatorStablePrompt(ctx, pe, promptRevision, sess, frame, history, templateRef, posture, settingsFP, turn, vars)
		if err != nil {
			return nil, err
		}
	} else {
		rendered, err = e.renderSystemPromptWithEngine(ctx, pe, sess, templateRef, vars)
		if err != nil {
			return nil, fmt.Errorf("system prompt %q: %w", templateRef, err)
		}
	}

	out := []api.Message{{Role: api.MessageRoleSystem, Content: rendered, ContextPinned: true}}

	// The standing prefix ends after the pre-history injects; its read point
	// survives a rewritten history.
	out, prefixIdx := e.appendPreHistorySystemInjects(ctx, sess, turn, out, 0)
	lastStableIdx := prefixIdx

	tailInjects, err := e.buildTailSystemInjects(
		ctx, sess, coordinatorSurface, frame, pendingKickIDs, history, turn,
	)
	if err != nil {
		return nil, err
	}

	if deps.EnrichHistory != nil {
		messages = deps.EnrichHistory(sess.ID, messages)
	}
	if coordinator && !sess.IsWorkerChild() {
		messages = e.interleaveSourceBriefs(ctx, sess, turn, messages)
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
		legMsgs, err := e.appendWorkerLegInject(ctx, sess, rendered, turn)
		if err != nil {
			return nil, err
		}
		out = append(out, legMsgs...)
	}

	if boardBlock != "" {
		out = append(out, api.Message{Role: api.MessageRoleSystem, Content: boardBlock})
	}

	e.markPromptCacheBreakpoints(out, prefixIdx, lastStableIdx)

	turn.Iteration++
	return transcript.Project(deduplicateProjectGuidance(out)), nil
}

func (e *AssemblyEngine) workspaceRootsForTurn(ctx context.Context, sess *api.Session, turn *TurnAssemblyScratch) ([]map[string]any, int, string, bool) {
	if turn != nil && turn.WorkspaceRootsLoaded {
		return turn.WorkspaceRoots, turn.WorkspaceRootCount, turn.WorkspaceActivePath, true
	}
	load := e.deps().WorkspaceRoots
	if load == nil {
		return nil, 0, "", false
	}
	roots, count, activePath := load(ctx, sess)
	if count < 0 {
		return nil, 0, "", false
	}
	if turn != nil {
		turn.WorkspaceRoots = roots
		turn.WorkspaceRootCount = count
		turn.WorkspaceActivePath = activePath
		turn.WorkspaceRootsLoaded = true
	}
	return roots, count, activePath, true
}

// markPromptCacheBreakpoints marks each stable boundary a provider may cache
// up to: the end of the standing prefix and the end of stable history. With
// no history the one boundary closes the standing prefix.
func (e *AssemblyEngine) markPromptCacheBreakpoints(out []api.Message, standingIdx, historyIdx int) {
	mark := func(idx int, tier api.PromptCacheTier) {
		if idx >= 0 && idx < len(out) && out[idx].PromptCacheBreakpoint == api.PromptCacheTierNone {
			out[idx].PromptCacheBreakpoint = tier
		}
	}
	mark(standingIdx, api.PromptCacheTierStanding)
	mark(historyIdx, api.PromptCacheTierHistory)
}

// appendWorkerLegInject builds changed worker context for the turn.
func (e *AssemblyEngine) appendWorkerLegInject(ctx context.Context, sess *api.Session, rendered string, turn *TurnAssemblyScratch) ([]api.Message, error) {
	deps := e.deps()
	legCtx, err := deps.WorkerContext.BuildWorkerPromptContext(sess.ID, sess)
	if err != nil {
		return nil, err
	}
	legCtx.Checklist = FilterChecklistDedup(legCtx.Checklist, ExtractProcessBullets(rendered))
	if deps.SiblingNoteDelivery != nil {
		page, err := deps.SiblingNoteDelivery.PrepareSiblingNotes(ctx, sess.ID, siblingNoteInjectCap)
		if err != nil {
			return nil, err
		}
		legCtx.SiblingNotes = page.Notes
		legCtx.SiblingNotesMore = page.More
		legCtx.SiblingNotesAfter = page.Cursor
		turn.LastSeenSiblingNoteID = page.Cursor
		turn.PendingSiblingNotes = page.Notes
	}
	if deps.PeerReservations != nil {
		legCtx.ReservedPaths = deps.PeerReservations.PeerReservations(ctx, sess.ID)
	}
	if deps.Injects == nil {
		return nil, fmt.Errorf("worker-leg inject: inject renderer not configured")
	}
	var guidance []api.Message
	if strings.TrimSpace(legCtx.AgentsMDMessage.Content) != "" {
		guidance = append(guidance, legCtx.AgentsMDMessage)
	}
	block, err := inject.RenderWorkerLegInject(ctx, deps.Injects, sess.ID, legCtx)
	if err != nil {
		return nil, fmt.Errorf("worker-leg inject: %w", err)
	}
	if strings.TrimSpace(block) == "" || (!e.shouldInjectWorkerLeg(turn, legCtx, block) && len(legCtx.SiblingNotes) == 0) {
		return guidance, nil
	}
	hostCtx := legCtx
	hostCtx.ScanDigest = nil
	hostCtx.HasPeerFindings = len(legCtx.SiblingNotes) > 0
	hostCtx.SiblingNotes = nil
	hostBlock, err := inject.RenderWorkerLegInject(ctx, deps.Injects, sess.ID, hostCtx)
	if err != nil {
		return nil, fmt.Errorf("worker-leg host projection: %w", err)
	}
	parts := workerLegContentParts(hostBlock, legCtx)
	return append(guidance, api.Message{
		Role:      api.MessageRoleSystem,
		Content:   block,
		Origin:    api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		ContentParts: parts,
	}), nil
}

func workerLegContentParts(hostBlock string, legCtx inject.WorkerLegContext) []api.MessageContentPart {
	parts := []api.MessageContentPart{{
		Content:   hostBlock,
		Origin:    api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
	}}
	if len(legCtx.ScanDigest) > 0 {
		parts = append(parts, api.MessageContentPart{
			Content: strings.Join(legCtx.ScanDigest, "\n"), Origin: api.MessageOriginRetrieval,
			Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted, Source: "scan_evidence",
		})
	}

	if peer := renderPeerNotesData(legCtx.SiblingNotes); peer != "" {
		parts = append(parts,
			api.MessageContentPart{
				Content: peer, Origin: api.MessageOriginPeerAgent,
				Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted, Source: "sibling_notes",
			},
		)
	}
	return parts
}

func renderPeerNotesData(notes []inject.SiblingNote) string {
	var lines []string
	for _, note := range notes {
		summary := strings.TrimSpace(note.Summary)
		if summary == "" {
			continue
		}
		line := summary
		if note.ID > 0 {
			line = fmt.Sprintf("finding %d: %s", note.ID, line)
		}
		if note.HasBody {
			line += " [detail: pack_board finding_id]"
		}
		if agent := strings.TrimSpace(note.Agent); agent != "" {
			line = agent + ": " + line
		}
		if ref := strings.TrimSpace(note.Ref); ref != "" {
			line += " (" + ref + ")"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// resolveTurnRoster combines workflow declarations with workspace and capability facts.
func (e *AssemblyEngine) resolveTurnRoster(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	turn *TurnAssemblyScratch,
) *inject.AgentRoster {
	profile, _ := e.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	_, rootCount, _, _ := e.workspaceRootsForTurn(ctx, sess, turn)
	repoKnownEmpty := false
	if path := strings.TrimSpace(sess.WorkspacePath); path != "" {
		repoKnownEmpty = e.repoKnownEmpty(ctx, path)
	}
	webSearchEnabled := true
	if fn := e.deps().WebSearchEnabled; fn != nil {
		webSearchEnabled = fn()
	}
	declared := frame.RunContext.AllowedAgents
	if len(declared) == 0 {
		declared = spawn.AmbientAllowedAgents()
	}
	roster := inject.ResolveAgentRoster(profile.SurfaceID, declared, rootCount, repoKnownEmpty, webSearchEnabled)
	return &roster
}

func (e *AssemblyEngine) prependCoordinatorRunInject(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	pendingKickIDs []string,
	turn *TurnAssemblyScratch,
	history []api.Message,
) ([]api.Message, error) {
	deps := e.deps()
	var out []api.Message
	runCtx := frame.RunContext
	if frame.Roster == nil {
		frame.Roster = e.resolveTurnRoster(ctx, sess, frame, history, turn)
	}

	// Workflow state is present only for bound sessions.
	if strings.TrimSpace(runCtx.WorkflowID) != "" {
		hintCodes := surface.StaticWorkflowHintCodes(runCtx, runCtx.HasComposeDraft)
		hintCodes = anchor.FilterSuppressedHintCodes(hintCodes, runCtx, pendingKickIDs...)
		snap := frame.Runtime
		injectData := inject.BuildActiveWorkflowInjectData(frame)
		injectData = inject.AttachGateObligations(ctx, injectData, deps.GateFeedback, runCtx.AdvanceWhenGateMet, snap.BlueprintBody)
		// The binding selects the template stem.
		block, err := inject.RenderActiveWorkflowInject(ctx, deps.Injects, sess.ID, frame, deps.WorkflowHints, hintCodes, deps.GateFeedback)
		if err != nil {
			return nil, fmt.Errorf("active-workflow inject (%s): %w", inject.ActiveWorkflowRenderStem(ctx, sess.ID), err)
		}
		if strings.TrimSpace(block) != "" && e.shouldInjectRunContext(turn, injectData, hintCodes, block) {
			out = append(out, api.Message{Role: api.MessageRoleSystem, Content: block})
		}
		if snap.Blueprint != nil {
			govBlock, err := inject.RenderBlueprintInject(ctx, deps.Injects, sess.ID, snap.Blueprint)
			if err != nil {
				return nil, fmt.Errorf("blueprint inject: %w", err)
			}
			if strings.TrimSpace(govBlock) != "" {
				out = append(out, api.Message{Role: api.MessageRoleSystem, Content: govBlock})
			}
		}
	}

	// Spawn-capable sessions always receive the active roster.
	if len(frame.Roster.Declared) > 0 {
		facts := frame.Roster.Facts
		spawnAgents := frame.Roster.Effective
		if len(spawnAgents) == 0 {
			return out, nil
		}
		var catalog *extpacks.EffectiveCatalog
		var toolProfiles []sandbox.ToolProfile
		if view := e.sessionCatalogView(ctx, sess); view != nil {
			catalog = view.Catalog
			toolProfiles = view.ToolProfiles
		}
		block, err := inject.RenderImplementSpawnInject(
			ctx, deps.Injects, sess.ID, spawnAgents, spawn.MaxInFlightTaskWorkers, facts.SurfaceID, facts.RootCount, facts.RepoKnownEmpty, facts.WebSearchEnabled,
			catalog, toolProfiles,
		)
		if err != nil {
			return nil, fmt.Errorf("implement-spawn inject: %w", err)
		}
		if strings.TrimSpace(block) != "" && e.taskOffered(sess, facts.SurfaceID, facts.RootCount) && e.shouldInjectSpawnRoster(turn, spawnAgents, facts.SurfaceID, facts.RootCount, facts.RepoKnownEmpty, facts.WebSearchEnabled, catalog, toolProfiles, block) {
			out = append(out, api.Message{Role: api.MessageRoleSystem, Content: block})
		}
	}
	return out, nil
}

func (e *AssemblyEngine) workerBoard(
	ctx context.Context,
	sess *api.Session,
	turn *TurnAssemblyScratch,
) (string, bool) {
	if e == nil || e.deps().Board == nil {
		return "", false
	}
	block, ok := e.deps().Board.WorkerBoard(ctx, sess)
	if !ok {
		return "", false
	}
	turn.BoardBlock = block
	turn.BoardKey = e.deps().Board.BoardInjectHash(sess.ID)
	return block, true
}

func (e *AssemblyEngine) shouldInjectSpawnRoster(
	turn *TurnAssemblyScratch,
	allowedAgents []string,
	surfaceID string,
	rootCount int,
	repoKnownEmpty bool,
	webSearchEnabled bool,
	catalog *extpacks.EffectiveCatalog,
	toolProfiles []sandbox.ToolProfile,
	block string,
) bool {
	key := inject.ImplementSpawnInjectFingerprint(allowedAgents, spawn.MaxInFlightTaskWorkers, surfaceID, rootCount, repoKnownEmpty, webSearchEnabled, catalog, toolProfiles)
	if turn.Iteration == 0 || key != turn.SpawnRosterKey {
		turn.SpawnRosterKey = key
		turn.SpawnRosterBlock = block
		return true
	}
	return false
}

func (e *AssemblyEngine) shouldInjectRunContext(
	turn *TurnAssemblyScratch,
	injectData inject.ActiveWorkflowInjectData,
	hintCodes []string,
	block string,
) bool {
	key := inject.ActiveWorkflowInjectFingerprint(injectData, hintCodes)
	if turn.Iteration == 0 || key != turn.RunContextKey {
		turn.RunContextKey = key
		turn.RunContextBlock = block
		return true
	}
	return false
}

func (e *AssemblyEngine) shouldInjectWorkerLeg(
	turn *TurnAssemblyScratch,
	legCtx inject.WorkerLegContext,
	block string,
) bool {
	stableKey := WorkerLegFingerprint(legCtx)
	volatileKey := WorkerLegVolatileFingerprint(legCtx)
	stableChanged := stableKey != turn.WorkerLegKey
	volatileDeliver := volatileKey != "" && volatileKey != turn.WorkerLegVolatileKey
	if turn.Iteration == 0 || stableChanged || volatileDeliver {
		turn.WorkerLegKey = stableKey
		turn.WorkerLegVolatileKey = volatileKey
		turn.WorkerLegBlock = block
		return true
	}
	return false
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
		e.saveExecutionModeFamily(context.Background(), sessionID, family)
	}
	e.cache.EndTurn(sessionID)
}

// PushModeTransitionCause records an explicit execution-mode entry for the active turn.
func (e *AssemblyEngine) PushModeTransitionCause(sessionID string, cause surface.ModeTransitionCause) {
	if e != nil {
		e.cache.PushModeTransitionCause(sessionID, cause)
	}
}

func (e *AssemblyEngine) prependTransitionInject(
	ctx context.Context,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	turn *TurnAssemblyScratch,
) (string, bool) {
	if e == nil || e.deps().Prompts == nil || sess == nil || turn == nil {
		return "", false
	}
	profile, implState := e.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	previous := e.loadExecutionModeState(ctx, sess.ID).LastFamily
	current := surface.ExecutionModeFamily(profile.SurfaceID)
	transition := surface.ComputeModeTransition(previous, current, turn.ModeTransitionCauses)
	if !inject.ShouldRenderTransitionInject(transition) {
		return "", false
	}
	key := inject.TransitionInjectKey(sess.ID, turn.PromptTurnSeq, transition)
	if turn.Iteration > 0 && key == turn.TransitionInjectKey && strings.TrimSpace(turn.TransitionInjectBlock) != "" {
		return turn.TransitionInjectBlock, true
	}
	if turn.Iteration > 0 && key == turn.TransitionInjectKey {
		return "", false
	}

	inj := e.deps().Injects
	if inj == nil {
		return "", false
	}
	surfaceVars := map[string]any{"project_dir": sess.WorkspacePath}
	if roots, count, activePath, ok := e.workspaceRootsForTurn(ctx, sess, turn); ok {
		surfaceVars["root_count"] = count
		surfaceVars["workspace_roots"] = roots
		if activePath != "" {
			surfaceVars["project_dir"] = activePath
		}
	}
	var toolProfiles []sandbox.ToolProfile
	turnSurface := prompts.SurfaceTurn{Loaded: e.loadedTools(sess)}
	if view := e.sessionCatalogView(ctx, sess); view != nil {
		toolProfiles = view.ToolProfiles
		turnSurface.Schemas = view.ToolSchemas
	}
	if err := prompts.MergeCoordinatorSurfacePathVars(profile.SurfaceID, toolProfiles, surfaceVars, turnSurface); err != nil {
		return "", false
	}
	if err := prompts.MergeCoordinatorPromptVars(
		profile.SurfaceID,
		prompts.ExecutionModePromptTransition{
			ExecutionMode:         transition.ExecutionMode,
			ExecutionModePrevious: transition.ExecutionModePrevious,
			ExecutionModeEntered:  transition.ExecutionModeEntered,
			ExecutionModeLeft:     transition.ExecutionModeLeft,
		},
		prompts.CoordinatorPromptGates{
			PendingOverlayPromote: len(implState.PendingOverlayIDs) > 0,
			VerifyRequired:        frame.RequiresEvidence("verify"),
			VerifyCommand:         implState.VerifyCommand,
		},
		surfaceVars,
	); err != nil {
		return "", false
	}
	block, err := anchor.RenderInform(ctx, anchor.InjectTransition, anchor.MatchContext{Surface: "coordinator", SessionID: sess.ID}, inj, surfaceVars)
	if err != nil || strings.TrimSpace(block) == "" {
		return "", false
	}
	turn.TransitionInjectKey = key
	turn.TransitionInjectBlock = strings.TrimSpace(block)
	return turn.TransitionInjectBlock, true
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

// taskOffered reports whether task is on this turn's model call: on the
// surface floor, or loaded by the turn ledger. The spawn roster only makes
// sense next to a task schema.
func (e *AssemblyEngine) taskOffered(sess *api.Session, surfaceID string, rootCount int) bool {
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID}, rootCount)
	if err != nil {
		return false
	}
	if plan.Immediate("task") {
		return true
	}
	return plan.Deferred("task") && e.loadedTools(sess)["task"]
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
