// Package platform defines cross-cutting host contracts.
package platform

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// PolicyContext is input for unified pre-tool policy evaluation.
type PolicyContext struct {
	ProfileID  string
	ToolAccess sandbox.ToolAccess
	ToolName   string
	ToolArgs   map[string]any
	// ResolvedFiles holds physical path identities for the invocation.
	ResolvedFiles []string
	// Dynamic tool identities come from the host registry.
	ApprovalCategory string
	ApprovalSubject  string
	// HostResources are exact ids resolved from the structured capability request.
	HostResources        []string
	HostResourceFamilies []string
	// Durable leases bind to ProjectID, not ProjectDir.
	ProjectID  string
	ProjectDir string
	SessionID  string
	ActionID   string
	// ParentSessionID scopes worker grants to the coordinator session.
	ParentSessionID string
	RootSessionID   string
	// ConfineRequest is the executor's per-action confinement.
	ConfineRequest confine.Request
	// SocketGrants are host-resolved AF_UNIX grants for policy/approval (optional).
	SocketGrants      []confine.SocketGrant
	SocketScopes      []string
	SocketGrantStates []string
	// AuthorizedSocketDigests are per-grant digests already authorized for this invocation.
	AuthorizedSocketDigests []string
	// AuthorizedDirectIP is true when a current-call direct-IP permit covers this invocation.
	AuthorizedDirectIP bool
	// DirectIPRequested is true when the would-be confinement selects NetworkDirectIP.
	DirectIPRequested    bool
	DirectIPVisibility   string
	DeclaredDestinations []string
	// BoundaryApprovalSatisfied is true when the current action's typed capability
	// checkpoint already resolved the approval result produced for this boundary.
	BoundaryApprovalSatisfied bool
	// PolicyWritesReviewed includes instruction-file authority in that resolved review.
	PolicyWritesReviewed bool
	// PackageExecution is the resolved package action.
	PackageExecution *packageexec.Execution
}

// ChatSessionID returns the chat's root session, the identity chat-lifetime
// authority is keyed on, falling back to ParentSessionID, then SessionID.
func (p PolicyContext) ChatSessionID() string {
	if root := strings.TrimSpace(p.RootSessionID); root != "" {
		return root
	}
	if parent := strings.TrimSpace(p.ParentSessionID); parent != "" {
		return parent
	}
	return strings.TrimSpace(p.SessionID)
}

// PolicyDecision is the outcome of policy evaluation before tool invoke.
type PolicyDecision struct {
	Allowed bool
	Blocked bool
	// The executor renders structured refusals into the tool envelope.
	RejectCode string
	RejectData map[string]any
	// BlockReason is the operator-facing cause recorded in the authz ledger.
	BlockReason        string
	Hints              []string
	RequiresApproval   bool
	ApprovalActionType string
	// The checkpoint reuses this gate result.
	Approval *hitl.ApprovalResult
	// Deferred tools load their schemas through request_tools.
	Deferred bool
}

// PolicyEngine evaluates tool invocations against rules, guidance, and profiles.
type PolicyEngine interface {
	Evaluate(ctx context.Context, eval PolicyContext) (*PolicyDecision, error)
}

// ToolFilter limits listed tools for a profile.
type ToolFilter struct {
	ProfileID        string
	ToolAccess       sandbox.ToolAccess
	Namespace        string
	ProjectRootCount int
}
