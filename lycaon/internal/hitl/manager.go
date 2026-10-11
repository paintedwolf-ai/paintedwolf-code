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

// ProposedAction describes what the agent wants to do, structured into cohesive host fact domains.
type ProposedAction struct {
	Invocation   ActionInvocation
	Scope        ActionScope
	Execution    ActionExecution
	Resources    ActionResources
	Mutations    ActionMutations
	Presentation ActionPresentation
	Sockets      ActionSockets
	Egress       ActionEgress
}

// ActionInvocation captures tool invocation facts.
type ActionInvocation struct {
	Tool          string
	Args          map[string]any
	Files         []string
	ResolvedFiles []string
	ActionID      string
}

// ActionScope binds an action to its project and session context.
type ActionScope struct {
	ProjectID          string
	ProjectDir         string
	SessionID          string
	RootSessionID      string
	SessionScratchRoot string
}

// ChatSession returns the chat that chat-scoped authority belongs to:
// RootSessionID when set, else the action's own SessionID.
func (s ActionScope) ChatSession() string {
	if s.RootSessionID != "" {
		return s.RootSessionID
	}
	return s.SessionID
}

// HasProjectIdentity reports whether the scope carries a project id.
func (s ActionScope) HasProjectIdentity() bool {
	return strings.TrimSpace(s.ProjectID) != ""
}

// ActionExecution captures execution boundaries, process targets, and packages.
type ActionExecution struct {
	Contained               Contained
	ProcessAccess           string
	ProcessTargets          []ApprovalTarget
	ExecutionBoundaryDigest string
	PackageExecution        *packageexec.Execution
}

// ActionResources carries host-resolved policy identity and resource targets.
type ActionResources struct {
	ApprovalCategory     string
	ApprovalSubject      string
	HostResources        []string
	HostResourceFamilies []string
}

// ActionMutations carries pending file and policy mutations.
type ActionMutations struct {
	FileChanges []api.ApprovalFileChange
	AgentPolicy []AgentPolicyTarget
}

// ActionPresentation captures card presentation facts.
type ActionPresentation struct {
	Command          string
	PresentationTool string
	EstimatedImpact  string
}

// ActionSockets captures UNIX domain socket grants and authorization facts.
type ActionSockets struct {
	SocketGrants            []confine.SocketGrant
	SocketScopes            []string
	SocketGrantStates       []string
	AuthorizedSocketDigests []string
}

// ActionEgress captures network egress facts.
type ActionEgress struct {
	AuthorizedDirectIP   bool
	DirectIPRequested    bool
	Visibility           string
	DeclaredDestinations []string
}

// ChatSession returns the chat that chat-scoped authority belongs to.
func (a ProposedAction) ChatSession() string {
	return a.Scope.ChatSession()
}

// HasProjectIdentity reports whether the action carries a project id.
func (a ProposedAction) HasProjectIdentity() bool {
	return a.Scope.HasProjectIdentity()
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
}
