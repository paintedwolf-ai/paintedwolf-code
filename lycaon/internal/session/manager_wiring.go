package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	sessioncatalog "github.com/lycaon/lycaon/internal/session/catalog"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	"github.com/lycaon/lycaon/internal/session/stream"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetInvocationRecorder wires durable invocation receipts.
func (m *Manager) SetInvocationRecorder(recorder invocation.Recorder) {
	m.invocations = recorder
}

// SetLimitsProvider wires dynamic limits from settings store.
func (m *Manager) SetLimitsProvider(p LimitsProvider) {
	m.limits = p
}

// SetCompactor wires context compaction for coordinator prompts.
func (m *Manager) SetCompactor(c compaction.ContextCompactor) {
	m.compactor = c
}

// SetPostureRegistry wires posture → tool profile resolution.
func (m *Manager) SetPostureRegistry(r *PostureRegistry) {
	m.postures = r
}

// SetAgentRegistry wires agent profile resolution.
func (m *Manager) SetAgentRegistry(r AgentProfileResolver) {
	m.agents = r
}

// SetDoomLoopGuard wires identical tool-call repetition blocking.
func (m *Manager) SetDoomLoopGuard(g loopguard.DoomLoopGuard) {
	m.doomLoop = g
	if g != nil && m.pageRegistry != nil {
		g.SetPageTargetResolver(m.pageRegistry.TargetURL)
	}
}

// ScanGuidanceHook prepends ephemeral scan guidance before LLM completion.
type ScanGuidanceHook interface {
	PrependGuidance(ctx context.Context, sessionID string, messages []api.Message) []api.Message
}

// SetScanGuidance wires ephemeral scan guidance injection.
func (m *Manager) SetScanGuidance(h ScanGuidanceHook) {
	if m != nil {
		m.scanGuidance = h
	}
}

// SetGroundingHook wires grounding checks for delegation coordinator sessions.
func (m *Manager) SetGroundingHook(h GroundingHook) {
	m.grounding = h
}

// SetRejectFormatter wires structured tool reject formatting.
func (m *Manager) SetRejectFormatter(f *guidance.StaticRejectFormatter) {
	m.rejectFmt = f
}

// SetToolInvoker wires invocation and profile-filtered definition metadata.
func (m *Manager) SetToolInvoker(inv tools.ToolInvoker, lister tools.ToolProfileLister) {
	m.toolInvoker = inv
	m.toolLister = lister
}

// SetPageRegistry wires session-scoped held browser pages.
func (m *Manager) SetPageRegistry(reg *pagesession.Registry) {
	if m != nil {
		m.pageRegistry = reg
		_ = m.RegisterSessionCleanup("browser-pages", 30, func(ctx context.Context, sessionID string) error {
			return reg.DisposeSession(ctx, sessionID)
		})
		if m.doomLoop != nil {
			m.doomLoop.SetPageTargetResolver(reg.TargetURL)
		}
	}
}

// SetEventPublisher wires SSE publish hooks for session, LLM, and cost topics.
func (m *Manager) SetEventPublisher(p *events.Publisher) {
	m.events = p
}

func (m *Manager) SetMaxIterations(n int) {
	if n > 0 {
		m.cfg.MaxIterations = n
	}
}

// SetEffectiveCatalogDeps wires session-scoped extension catalog resolution.
func (m *Manager) SetEffectiveCatalogDeps(moduleRoot string, boot *extpacks.EffectiveCatalog, surfaces *settings.TrustSurfacesStore) {
	if m == nil {
		return
	}
	m.trustSurfaces = surfaces
	m.catalog.Configure(moduleRoot, boot, surfaces)
}

func (m *Manager) Catalog() *sessioncatalog.Service {
	if m == nil {
		return nil
	}
	return &m.catalog
}

// SetSandboxPathGrantRuntime wires write-root guard state.
func (m *Manager) SetSandboxPathGrantRuntime(runtime *approvalstate.SandboxPathGrantRuntime) {
	if m != nil {
		m.writeRootRuntime = runtime
	}
}

// SetSandboxListenRuntime wires listener guard state.
func (m *Manager) SetSandboxListenRuntime(runtime *approvalstate.SandboxPortGrantRuntime) {
	if m != nil {
		m.listenRuntime = runtime
	}
}

func (m *Manager) SetSandboxLoopbackRuntime(runtime *approvalstate.SandboxPortGrantRuntime) {
	if m != nil {
		m.loopbackRuntime = runtime
	}
}

// SetToolApprovalCoalesce wires approval coalescing state.
func (m *Manager) SetToolApprovalCoalesce(coalesce *approvalstate.ToolApprovalCoalesce) {
	if m != nil {
		m.toolApprovalCoalesce = coalesce
	}
}

// SetGateRepeatLedger wires approval repeat state.
func (m *Manager) SetGateRepeatLedger(ledger *approvalstate.GateRepeatLedger) {
	if m != nil {
		m.gateRepeatLedger = ledger
	}
}

func (m *Manager) PromptState() *promptstate.State {
	if m == nil {
		return nil
	}
	return &m.promptState
}

func (m *Manager) Streams() *stream.State {
	m.streamsOnce.Do(func() { m.streams = stream.New(m.store) })
	return m.streams
}

// SetVisualStore wires the session-tree visual artifact store.
func (m *Manager) SetVisualStore(store visual.Store) {
	if m == nil {
		return
	}
	m.visual = store
}

// RuleEvaluator evaluates scaffold rules before tool execution.
type RuleEvaluator interface {
	Evaluate(ctx context.Context, eval rules.EvalContext) (*rules.RuleOutcome, error)
}

// SetRuleEngine wires plan-mode tool gating.
func (m *Manager) SetRuleEngine(e RuleEvaluator) {
	m.rules = e
}

// GroundingHook runs grounding checks for delegation coordinator sessions.
type GroundingHook interface {
	IsEscalated(sessionID string) bool
	AfterPrompt(ctx context.Context, sessionID string, lastTools []string) error
	Reset(sessionID string)
}

// OverlayPromoter lands or closes an overlay and rebases or orphans its stacked children.
type OverlayPromoter interface {
	PromoteOverlay(ctx context.Context, sessionID, overlayID string, in api.PromoteOverlayInput) (api.WorkerMergeResult, error)
	RejectOverlay(ctx context.Context, sessionID, overlayID, reason string) (api.OverlayRejectOutcome, error)
	RebaseChildren(ctx context.Context, sessionID, parentOverlayID string) ([]api.OverlayRebaseOutcome, error)
}
