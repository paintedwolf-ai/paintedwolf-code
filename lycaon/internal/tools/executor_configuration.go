package tools

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/destconfig"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/pkgregistry"
)

// SetRejectFormatter renders rejects outside an OAR Decision.
func (e *DefaultToolExecutor) SetRejectFormatter(f *guidance.StaticRejectFormatter) {
	if e != nil {
		e.rejectFmt = f
	}
}

// renderReject is the single exit for a structured reject leaving Invoke.
func (e *DefaultToolExecutor) renderReject(rej *ToolReject) error {
	if e == nil {
		return RenderReject(rej, nil)
	}
	return RenderReject(rej, e.rejectFmt)
}

// SessionHostLedger records hosts observed across page and command egress.
type SessionHostLedger interface {
	SessionVisitedHosts(chatSessionID string) map[string]struct{}
	RecordHostVisit(ctx context.Context, chatSessionID, host string) error
}

// SetSessionHostLedger wires the visited-host record for the pre-dial gate.
func (e *DefaultToolExecutor) SetSessionHostLedger(l SessionHostLedger) {
	if e != nil {
		e.hostLedger = l
	}
}

// SetDestinationConfig wires the user-configuration lookup for destinations.
func (e *DefaultToolExecutor) SetDestinationConfig(r *destconfig.Registry) {
	if e != nil {
		e.destinations = r
	}
}

// SetPackageRegistries wires the public registry catalog for destinations.
func (e *DefaultToolExecutor) SetPackageRegistries(c *pkgregistry.Catalog) {
	if e != nil {
		e.registries = c
	}
}

// SetSecretExposureSource wires the session credential-exposure lookup.
func (e *DefaultToolExecutor) SetSecretExposureSource(fn func(ctx context.Context, chatSessionID string) (bool, error)) {
	if e != nil {
		e.secretExposure = fn
	}
}

// SetUntrustedIngestionSource wires the session external-content lookup.
func (e *DefaultToolExecutor) SetUntrustedIngestionSource(fn func(ctx context.Context, chatSessionID string) (bool, error)) {
	if e != nil {
		e.untrustedIngestion = fn
	}
}

// SetBlockPlane installs the OAR Emit/Binding block plane.
func (e *DefaultToolExecutor) SetBlockPlane(bp *BlockPlane) {
	if e != nil {
		e.blockPlane = bp
	}
}

// SetMCPCatalog wires MCP structural observation for OAR.
func (e *DefaultToolExecutor) SetMCPCatalog(c oar.MCPCatalogView) {
	if e != nil && e.blockPlane != nil {
		e.blockPlane.MCPCatalog = c
	}
}

// SetCheckpointManager routes tool and egress approvals through the same checkpoint flow.
func (e *DefaultToolExecutor) SetCheckpointManager(mgr hitl.CheckpointManager, gate hitl.ApprovalGate) {
	if e != nil {
		e.checkpointMgr = mgr
		e.approvalGate = gate
		confine.SetEgressResolver(e.resolveEgress)
	}
}

// SetToolApprovalCoalesce wires mint/join spam guards for pending tool_approval asks.
func (e *DefaultToolExecutor) SetToolApprovalCoalesce(rt ToolApprovalCoalesce) {
	if e != nil {
		e.approvalCoalesce = rt
	}
}

// SetGateRepeatLedger wires reason-keyed repeat counting for tool_approval mints.
func (e *DefaultToolExecutor) SetGateRepeatLedger(rt GateRepeatLedger) {
	if e != nil {
		e.gateRepeat = rt
	}
}

// SetConsequenceDeriver wires presentation-only band derivation at checkpoint mint.
func (e *DefaultToolExecutor) SetConsequenceDeriver(d ConsequenceDeriver) {
	if e != nil {
		e.consequence = d
	}
}

// SetEgressPostureSource wires the ask-line lookup used by pre-dial decisions.
func (e *DefaultToolExecutor) SetEgressPostureSource(fn func(projectDir string) gate.Posture) {
	if e != nil {
		e.egressPostureFor = fn
	}
}

// SetApprovalExplainer wires reviewed explanation copy for tool_approval checkpoints.
func (e *DefaultToolExecutor) SetApprovalExplainer(explainer ApprovalExplainer) {
	if e != nil {
		e.approvalExplainer = explainer
	}
}

// SetBackgroundCommandResolver wires exact host-recorded process detail into
// command_stop approval cards without exposing the handle as the action summary.
func (e *DefaultToolExecutor) SetBackgroundCommandResolver(resolver BackgroundCommandResolver) {
	if e != nil {
		e.backgroundCommand = resolver
	}
}

// SetAuthzRecorder wires tamper-evident tool_denied recording.
func (e *DefaultToolExecutor) SetAuthzRecorder(r authzledger.Recorder) {
	if e != nil {
		e.authzRecorder = r
	}
}

// SetApprovalOutcomeRenderer wires agent-facing copy for checkpoints that resolve
// without approval (denied / expired).
func (e *DefaultToolExecutor) SetApprovalOutcomeRenderer(r ApprovalOutcomeRenderer) {
	if e != nil {
		e.approvalOutcome = r
	}
}

// SetAIRationaleAttacher wires optional host lite-model rationale patching on
// tool_approval checkpoints (awaitApproval only; not egress).
func (e *DefaultToolExecutor) SetAIRationaleAttacher(a AIRationaleAttacher) {
	if e != nil {
		e.aiRationale = a
	}
}

// outcomeMessage renders the outcome or returns its code when no renderer is configured.
func (e *DefaultToolExecutor) outcomeMessage(code string, context map[string]any) string {
	if e.approvalOutcome != nil {
		if msg := strings.TrimSpace(e.approvalOutcome.ApprovalOutcome(code, context)); msg != "" {
			return msg
		}
	}
	return code
}
