package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetInvocationRecorder wires durable invocation receipts.
func (m *Manager) SetInvocationRecorder(recorder invocation.Recorder) {
	m.invocations = recorder
	m.Interruptions.SetRecorder(recorder)
}

// SetDoomLoopGuard wires identical tool-call repetition blocking.
func (m *Manager) SetDoomLoopGuard(g loopguard.DoomLoopGuard) {
	m.doomLoop = g
	m.ToolPolicy.SetDoomLoopGuard(g)
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
	m.Runner.SetGrounding(h)
	m.Runner.PostTurn.SetGrounding(h)
	m.Workers.Summaries.SetGrounding(h)
	m.Workers.Cancellations.SetGrounding(h)
}

// SetRejectFormatter wires structured tool reject formatting.
func (m *Manager) SetRejectFormatter(f *guidance.StaticRejectFormatter) {
	m.rejectFmt = f
	m.Guards.SetRejects(f, m.toolRejectFormatter)
	m.Guidance.SetRejectFormatter(f)
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
	m.Runner.Status.SetPublisher(p)
	m.Loading.SetPublisher(p)
	m.Chats.SetPublisher(p)
	m.Naming.SetPublisher(p)
	m.Drafts.SetPublisher(p)
	m.Runner.SubmissionState.SetPublisher(p)
	m.Transcript.SetPublisher(p)
	m.Rewinds.SetPublisher(p)
	m.Runner.Clocks.Publisher = p
}

// SetEffectiveCatalogDeps wires session-scoped extension catalog resolution.
func (m *Manager) SetEffectiveCatalogDeps(moduleRoot string, boot *extpacks.EffectiveCatalog, surfaces *settings.TrustSurfacesStore) {
	if m == nil {
		return
	}
	m.trustSurfaces = surfaces
	m.Profiles.SetTrustSurfaces(surfaces)
	m.Workspace.SetTrust(surfaces)
	m.Catalog.Configure(moduleRoot, boot, surfaces)
}

// SetSandboxPathGrantRuntime wires write-root guard state.
func (m *Manager) SetSandboxPathGrantRuntime(runtime *approvalstate.SandboxPathGrantRuntime) {
	if m != nil {
		m.writeRootRuntime = runtime
		m.Runner.Instructions.SetIntentBoundaries(m.toolApprovalCoalesce, m.gateRepeatLedger, m.writeRootRuntime, m.listenRuntime, m.loopbackRuntime)
	}
}

// SetSandboxListenRuntime wires listener guard state.
func (m *Manager) SetSandboxListenRuntime(runtime *approvalstate.SandboxPortGrantRuntime) {
	if m != nil {
		m.listenRuntime = runtime
		m.Runner.Instructions.SetIntentBoundaries(m.toolApprovalCoalesce, m.gateRepeatLedger, m.writeRootRuntime, m.listenRuntime, m.loopbackRuntime)
	}
}

func (m *Manager) SetSandboxLoopbackRuntime(runtime *approvalstate.SandboxPortGrantRuntime) {
	if m != nil {
		m.loopbackRuntime = runtime
		m.Runner.Instructions.SetIntentBoundaries(m.toolApprovalCoalesce, m.gateRepeatLedger, m.writeRootRuntime, m.listenRuntime, m.loopbackRuntime)
	}
}

// SetToolApprovalCoalesce wires approval coalescing state.
func (m *Manager) SetToolApprovalCoalesce(coalesce *approvalstate.ToolApprovalCoalesce) {
	if m != nil {
		m.toolApprovalCoalesce = coalesce
		m.Runner.Instructions.SetIntentBoundaries(m.toolApprovalCoalesce, m.gateRepeatLedger, m.writeRootRuntime, m.listenRuntime, m.loopbackRuntime)
	}
}

// SetGateRepeatLedger wires approval repeat state.
func (m *Manager) SetGateRepeatLedger(ledger *approvalstate.GateRepeatLedger) {
	if m != nil {
		m.gateRepeatLedger = ledger
		m.Runner.Instructions.SetIntentBoundaries(m.toolApprovalCoalesce, m.gateRepeatLedger, m.writeRootRuntime, m.listenRuntime, m.loopbackRuntime)
	}
}

// SetVisualStore wires the session-tree visual artifact store.
func (m *Manager) SetVisualStore(store visual.Store) {
	if m == nil {
		return
	}
	m.visual = store
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
