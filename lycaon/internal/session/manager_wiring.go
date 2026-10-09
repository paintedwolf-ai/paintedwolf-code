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
func (m *Host) SetInvocationRecorder(recorder invocation.Recorder) {
	m.Coordinator.Tools.Invocations = recorder
	m.Coordinator.Tools.Invocations = recorder

	m.Stops.Recovery.SetRecorder(recorder)
}

// SetDoomLoopGuard wires identical tool-call repetition blocking.
func (m *Host) SetDoomLoopGuard(g loopguard.DoomLoopGuard) {
	m.Coordinator.Nudging.DoomLoop = g

	m.ToolPolicy.SetDoomLoopGuard(g)
	if g != nil && m.Coordinator.Context.Pages != nil {
		g.SetPageTargetResolver(m.Coordinator.Context.Pages.TargetURL)
	}
}

// ScanGuidanceHook prepends ephemeral scan guidance before LLM completion.
type ScanGuidanceHook interface {
	PrependGuidance(ctx context.Context, sessionID string, messages []api.Message) []api.Message
}

// SetScanGuidance wires ephemeral scan guidance injection.
func (m *Host) SetScanGuidance(h ScanGuidanceHook) {
	if m != nil {
		m.Coordinator.Assembly.Scan = h

	}
}

// SetGroundingHook wires grounding checks for delegation coordinator sessions.
func (m *Host) SetGroundingHook(h GroundingHook) {
	m.Coordinator.Loop.Grounding = h

	m.Runner.SetGrounding(h)
	m.Runner.PostTurn.SetGrounding(h)
	m.Workers.Summaries.SetGrounding(h)
	m.Workers.Cancellations.SetGrounding(h)
}

// SetRejectFormatter wires structured tool reject formatting.
func (m *Host) SetRejectFormatter(f *guidance.StaticRejectFormatter) {
	m.Coordinator.Completion.Rejects = f

	m.Coordinator.Guards.SetRejects(f, m.Coordinator.Feedback.Rejects)
	m.Coordinator.Guidance.SetRejectFormatter(f)
}

// SetPageRegistry wires session-scoped held browser pages.
func (m *Host) SetPageRegistry(reg *pagesession.Registry) {
	if m != nil {
		m.Coordinator.Context.Pages = reg

		_ = m.Resources.RegisterCleanup("browser-pages", 30, func(ctx context.Context, sessionID string) error {
			return reg.DisposeSession(ctx, sessionID)
		})
		if m.Coordinator.Nudging.DoomLoop != nil {
			m.Coordinator.Nudging.DoomLoop.SetPageTargetResolver(reg.TargetURL)
		}
	}
}

// SetEventPublisher wires SSE publish hooks for session, LLM, and cost topics.
func (m *Host) SetEventPublisher(p *events.Publisher) {
	m.Coordinator.Projection.Events = p
	m.Coordinator.Loop.Events = p

	m.Workers.Workspaces.SetPublisher(p)
	m.Runner.Status.SetPublisher(p)
	m.Coordinator.Loading.SetPublisher(p)
	m.Chats.SetPublisher(p)
	m.Chats.Naming.SetPublisher(p)
	m.Chats.Drafts.SetPublisher(p)
	m.Runner.SubmissionState.SetPublisher(p)
	m.Runner.Transcript.SetPublisher(p)
	m.Chats.Rewinds.SetPublisher(p)
	m.Runner.Clocks.Publisher = p
}

// SetEffectiveCatalogDeps wires session-scoped extension catalog resolution.
func (m *Host) SetEffectiveCatalogDeps(moduleRoot string, boot *extpacks.EffectiveCatalog, surfaces *settings.TrustSurfacesStore) {
	if m == nil {
		return
	}

	m.Profiles.SetTrustSurfaces(surfaces)
	m.Workspace.SetTrust(surfaces)
	m.Catalog.Configure(moduleRoot, boot, surfaces)
}

// SetSandboxPathGrantRuntime wires write-root guard state.
func (m *Host) SetSandboxPathGrantRuntime(runtime *approvalstate.SandboxPathGrantRuntime) {
	if m != nil {
		m.Resources.Authority.Writes = runtime
		m.Resources.Authority.Writes = runtime
		m.Runner.Instructions.SetIntentBoundaries(m.Resources.Tools.Approvals, m.Resources.Tools.Repeat, m.Resources.Authority.Writes, m.Resources.Authority.Listen, m.Resources.Authority.Loopback)
	}
}

// SetSandboxListenRuntime wires listener guard state.
func (m *Host) SetSandboxListenRuntime(runtime *approvalstate.SandboxPortGrantRuntime) {
	if m != nil {
		m.Resources.Authority.Listen = runtime
		m.Resources.Authority.Listen = runtime
		m.Runner.Instructions.SetIntentBoundaries(m.Resources.Tools.Approvals, m.Resources.Tools.Repeat, m.Resources.Authority.Writes, m.Resources.Authority.Listen, m.Resources.Authority.Loopback)
	}
}

func (m *Host) SetSandboxLoopbackRuntime(runtime *approvalstate.SandboxPortGrantRuntime) {
	if m != nil {
		m.Resources.Authority.Loopback = runtime
		m.Resources.Authority.Loopback = runtime
		m.Runner.Instructions.SetIntentBoundaries(m.Resources.Tools.Approvals, m.Resources.Tools.Repeat, m.Resources.Authority.Writes, m.Resources.Authority.Listen, m.Resources.Authority.Loopback)
	}
}

// SetToolApprovalCoalesce wires approval coalescing state.
func (m *Host) SetToolApprovalCoalesce(coalesce *approvalstate.ToolApprovalCoalesce) {
	if m != nil {
		m.Resources.Tools.Approvals = coalesce
		m.Resources.Tools.Approvals = coalesce
		m.Runner.Instructions.SetIntentBoundaries(m.Resources.Tools.Approvals, m.Resources.Tools.Repeat, m.Resources.Authority.Writes, m.Resources.Authority.Listen, m.Resources.Authority.Loopback)
	}
}

// SetGateRepeatLedger wires approval repeat state.
func (m *Host) SetGateRepeatLedger(ledger *approvalstate.GateRepeatLedger) {
	if m != nil {
		m.Resources.Tools.Repeat = ledger
		m.Resources.Tools.Repeat = ledger
		m.Runner.Instructions.SetIntentBoundaries(m.Resources.Tools.Approvals, m.Resources.Tools.Repeat, m.Resources.Authority.Writes, m.Resources.Authority.Listen, m.Resources.Authority.Loopback)
	}
}

// SetVisualStore wires the session-tree visual artifact store.
func (m *Host) SetVisualStore(store visual.Store) {
	if m == nil {
		return
	}
	m.Coordinator.Tools.Visual = store

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
