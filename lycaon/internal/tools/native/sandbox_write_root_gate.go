package native

import "context"

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
}

// SandboxWriteRootResult is the gate outcome after optional HITL.
type SandboxWriteRootResult struct {
	// Raised reports whether an approval was awaited.
	Raised bool
	// Authorized reports whether the root was granted.
	Authorized bool
	// Denied reports a rejected or expired approval.
	Denied bool
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
	ToolName         string
	Command          string
	ProposedReadPath string
	// ReadDenyPaths are additional host-resolved exclusions for this action.
	ReadDenyPaths []string
}

// SandboxReadPathResult is the gate outcome after optional HITL.
type SandboxReadPathResult struct {
	Raised     bool
	Authorized bool
	Denied     bool
	// ProposedReadPath is the approved path.
	ProposedReadPath string
	// UserGuidance is explicit composer direction attached to a denial.
	UserGuidance string
}

// SandboxWriteRootGate resolves explicit path authority before spawn and
// exposes the chat-scoped overlays used by command confinement. Write roots
// and read paths are separate namespaces: a grant in one never covers the other.
type SandboxWriteRootGate interface {
	Authorize(ctx context.Context, in SandboxWriteRootAsk) (SandboxWriteRootResult, error)
	SessionWriteRoots(ctx context.Context, sessionID, parentSessionID string) []string
	AuthorizeRead(ctx context.Context, in SandboxReadPathAsk) (SandboxReadPathResult, error)
	SessionReadPaths(ctx context.Context, sessionID, parentSessionID string) []string
}
