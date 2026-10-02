// Package hitl evaluates approval policy and manages human checkpoints.
package hitl

import (
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/pkg/api"
)

// DecisionType is the kind of human decision requested.
type DecisionType string

const (
	DecisionTypeApprove DecisionType = "approve"
)

// DefaultCheckpointTimeout is the unanswered-checkpoint expiry. Elapsed
// checkpoints resolve denied so the turn unblocks.
const DefaultCheckpointTimeout = 24 * time.Hour

// DecisionStatus is the lifecycle state of a HITL decision.
type DecisionStatus string

const (
	DecisionStatusPending  DecisionStatus = "pending"
	DecisionStatusApproved DecisionStatus = "approved"
	DecisionStatusRejected DecisionStatus = "rejected"
	DecisionStatusExpired  DecisionStatus = "expired"
	DecisionStatusCanceled DecisionStatus = "canceled"
)

// ProposedAction describes what the agent wants to do.
type ProposedAction struct {
	ProcessAccess           string
	ProcessTargets          []ApprovalTarget
	ExecutionBoundaryDigest string
	FileChanges             []api.ApprovalFileChange
	// AgentPolicy is each changed file a project trust surface loads.
	AgentPolicy []AgentPolicyTarget
	Tool        string
	Args        map[string]any
	Files       []string
	// ResolvedFiles binds declared paths to their physical invocation targets.
	ResolvedFiles []string
	// ApprovalCategory and ApprovalSubject carry host-resolved policy identity.
	ApprovalCategory string
	ApprovalSubject  string
	// HostResources identifies policy subjects without granting access.
	HostResources []string
	// HostResourceFamilies groups policy subjects; grants bind concrete IDs.
	HostResourceFamilies []string
	// Command is display text; Args retains the exact invocation.
	Command string
	// PresentationTool overrides the card label without changing tool identity.
	PresentationTool string
	EstimatedImpact  string
	// ProjectID is the identity a durable lease binds to.
	ProjectID string
	// ProjectDir is the active folder for path matching and coverage copy.
	ProjectDir string
	SessionID  string
	// RootSessionID identifies the top-level chat.
	RootSessionID string
	// SessionScratchRoot is the invoking session's own scratch workspace. It
	// holds whether or not an OS boundary applies, so it is not read from Contained.
	SessionScratchRoot string
	// Contained is the executor's confinement result.
	Contained Contained
	// SocketGrants are host-resolved AF_UNIX grants for this invocation (not raw model paths).
	SocketGrants      []confine.SocketGrant
	SocketScopes      []string
	SocketGrantStates []string
	// AuthorizedSocketDigests lists per-grant digests already covered by chat scope or
	// a current-call permit for this invocation.
	AuthorizedSocketDigests []string
	// AuthorizedDirectIP is true when a current-call direct-IP permit covers this invocation.
	AuthorizedDirectIP bool
	// ActionID is the host-issued tool-call/action identity used for event attribution.
	ActionID string
	// DirectIPRequested and its presentation context are host-derived capability facts.
	DirectIPRequested    bool
	Visibility           string
	DeclaredDestinations []string
	// PackageExecution binds approval to the resolved package action.
	PackageExecution *packageexec.Execution
}

// ChatSession returns the chat that chat-scoped authority belongs to:
// RootSessionID when set, else the action's own SessionID.
func (a ProposedAction) ChatSession() string {
	if a.RootSessionID != "" {
		return a.RootSessionID
	}
	return a.SessionID
}

// HasProjectIdentity reports whether the action carries a project id.
func (a ProposedAction) HasProjectIdentity() bool {
	return strings.TrimSpace(a.ProjectID) != ""
}

// DecisionResult is the human resolution payload.
type DecisionResult struct {
	Approved bool
	// OptionID is the exact plan option selected by the human.
	OptionID string
	Comments string
	// RedactSecrets selects the host-authored plan’s redacted outbound payload.
	RedactSecrets bool
	// TrackSecrets stores detected values and replaces them with capabilities.
	TrackSecrets bool
	// GrantIDs are every reusable authority installed by the selected option.
	// A composed option can install more than one and they revoke as one choice.
	GrantIDs   []string
	GrantScope ApprovalGrantScope
	GrantTitle string
	// AttestationID names the verified presence that released held values.
	AttestationID string
}
