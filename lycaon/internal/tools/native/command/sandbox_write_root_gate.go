package command

import (
	"context"

	"github.com/lycaon/lycaon/internal/settings"
)

// SandboxDenialCause identifies why a path preflight refused authority.
type SandboxDenialCause string

const (
	DenialCauseNone               SandboxDenialCause = ""
	DenialCauseUserDeclined       SandboxDenialCause = "user_declined"
	DenialCausePriorUserDeclined  SandboxDenialCause = "prior_user_declined"
	DenialCauseApprovalRule       SandboxDenialCause = "approval_rule"
	DenialCauseControlPlanePolicy SandboxDenialCause = "control_plane_policy"
)

// SandboxWriteRootAsk is the structured pre-spawn input for write authority.
type SandboxWriteRootAsk struct {
	SessionID       string
	ParentSessionID string
	ProjectID       string
	ToolCallID      string
	ProjectDir      string
	// ToolName is the argv-running tool.
	ToolName          string
	Command           string
	ProposedWriteRoot string
	// SessionScratchRoot is the invoking session's scratch folder, the same
	// fact its confinement and control-plane verdict carry.
	SessionScratchRoot string
	SessionOutputRoot  string
}

// SandboxWriteRootResult is the gate outcome after optional HITL.
type SandboxWriteRootResult struct {
	// Raised reports whether an approval was awaited.
	Raised bool
	// Authorized reports whether the root was granted.
	Authorized bool
	// Denied reports a rejected ask, deny rule, or control-plane block.
	Denied bool
	// DenialCause distinguishes rule, control-plane, and user denials.
	DenialCause SandboxDenialCause
	// DeniedRule is the matched deny rule when DenialCause == DenialCauseApprovalRule.
	DeniedRule *settings.ApprovalRule
	// ProposedWriteRoot is the approved root.
	ProposedWriteRoot string
	// UserGuidance is explicit composer direction attached to a denial.
	UserGuidance string
}

// SandboxReadPathAsk is the structured pre-spawn input for read authority.
type SandboxReadPathAsk struct {
	SessionID       string
	ParentSessionID string
	ProjectID       string
	ToolCallID      string
	ProjectDir      string
	// ToolName is the argv-running tool.
	ToolName           string
	Command            string
	ProposedReadPath   string
	SessionScratchRoot string
	SessionOutputRoot  string
	// ReadDenyPaths are additional host-resolved exclusions for this action.
	ReadDenyPaths []string
}

// SandboxReadPathResult is the gate outcome after optional HITL.
type SandboxReadPathResult struct {
	Raised      bool
	Authorized  bool
	Denied      bool
	DenialCause SandboxDenialCause
	DeniedRule  *settings.ApprovalRule
	// ProposedReadPath is the approved path.
	ProposedReadPath string
	// UserGuidance is explicit composer direction attached to a denial.
	UserGuidance string
}

// SandboxWriteRootGate resolves explicit path authority before spawn and
// exposes the chat-scoped overlays used by command confinement. Write roots
// and read paths are separate namespaces: a grant in one never covers the other.
type SandboxWriteRootGate interface {
	SharedTempReviewer
	Authorize(ctx context.Context, in SandboxWriteRootAsk) (SandboxWriteRootResult, error)
	SessionWriteRoots(ctx context.Context, sessionID, parentSessionID string) []string
	AuthorizeRead(ctx context.Context, in SandboxReadPathAsk) (SandboxReadPathResult, error)
	SessionReadPaths(ctx context.Context, sessionID, parentSessionID string) []string
}
