package toolexecution

import (
	"context"
)

// SetRejectFormatter renders rejects outside an OAR Decision.

// renderReject is the single exit for a structured reject leaving Invoke.

// SessionHostLedger records hosts observed across page and command egress.
type SessionHostLedger interface {
	SessionVisitedHosts(chatSessionID string) map[string]struct{}
	RecordHostVisit(ctx context.Context, chatSessionID, host string) error
}

// SetSessionHostLedger wires the visited-host record for the pre-dial gate.

// SetDestinationConfig wires the user-configuration lookup for destinations.

// SetPackageRegistries wires the public registry catalog for destinations.

// SetSecretExposureSource wires the session credential-exposure lookup.

// SetUntrustedIngestionSource wires the session external-content lookup.

// SetBlockPlane installs the OAR Emit/Binding block plane.

// SetMCPCatalog wires MCP structural observation for OAR.

// SetCheckpointManager routes tool and egress approvals through the same checkpoint flow.

// SetToolApprovalCoalesce wires mint/join spam guards for pending tool_approval asks.

// SetGateRepeatLedger wires reason-keyed repeat counting for tool_approval mints.

// SetConsequenceDeriver wires presentation-only band derivation at checkpoint mint.

// SetEgressPostureSource wires the ask-line lookup used by pre-dial decisions.

// SetApprovalExplainer wires reviewed explanation copy for tool_approval checkpoints.

// SetBackgroundCommandResolver wires exact host-recorded process detail into
// command_stop approval cards without exposing the handle as the action summary.

// SetAuthzRecorder wires tamper-evident tool_denied recording.

// SetApprovalOutcomeRenderer wires agent-facing copy for checkpoints that resolve
// without approval (denied / expired).

// SetAIRationaleAttacher wires optional host lite-model rationale patching on
// tool_approval checkpoints (awaitApproval only; not egress).

// outcomeMessage renders the outcome or returns its code when no renderer is configured.
