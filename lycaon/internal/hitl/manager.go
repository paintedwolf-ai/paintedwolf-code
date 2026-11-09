// Package hitl evaluates approval policy and manages human checkpoints.
package hitl

import (
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/toolcontract"
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
	NativeInputs  *NativeActionInputs `json:"-"`
	BoundStages   []toolcontract.ExecutionStage
	Tool          string
	Args          map[string]any
	Files         []string
	ResolvedFiles []string
	// NativePathTargets are the bound command owner's file operands. A present
	// empty slice means the native form has no file operand.
	NativePathTargets []NativePathTarget
	// ApprovedFileAccess carries exact authority already approved for this invocation.
	ApprovedFileAccess []GrantedPathDelta `json:"-"`
	// CurrentActionReview carries the exact reasons approved by a human checkpoint for this invocation.
	CurrentActionReview *CurrentActionReview `json:"-"`
	ActionID            string
}

// CurrentActionReview records the exact policy reasons and subjects reviewed
// and approved by a human checkpoint for the current invocation.
type CurrentActionReview struct {
	SessionID      string
	ToolCallID     string
	InvocationID   string
	ActionID       string
	ActionKey      string
	Cited          []gate.Fact
	ReasonSegments []string
	Detection      *DetectionMatch
	MatchedRules   []ApprovalRuleMatch
	AgentPolicy    []AgentPolicyTarget
}

// ActionKeyForCurrentReview returns the action identity for current-call review
// matching; ExecutionBoundaryDigest is verified separately against confine.Request.
func ActionKeyForCurrentReview(action ProposedAction) string {
	action.Execution.ExecutionBoundaryDigest = ""
	return GrantKey(action)
}

// CurrentActionReviewCovers reports whether the current invocation's human
// review already covers the evaluated decision, detection, and rule matches.
func CurrentActionReviewCovers(
	action ProposedAction,
	decision *gate.Decision,
	citation *DetectionMatch,
	matchedRules []ApprovalRuleMatch,
) bool {
	review := action.Invocation.CurrentActionReview
	if review == nil || decision == nil {
		return false
	}
	if review.SessionID == "" || review.SessionID != action.Scope.SessionID ||
		review.ActionID != action.Invocation.ActionID {
		return false
	}
	actionKey := ActionKeyForCurrentReview(action)
	if actionKey == "" || review.ActionKey != actionKey {
		return false
	}
	gates := decision.Gates()
	if len(gates) == 0 || len(decision.Cited) == 0 || len(review.Cited) == 0 {
		return false
	}
	for _, fact := range decision.Cited {
		if !CurrentActionReviewAllowsFact(fact) || !slices.Contains(review.Cited, fact) {
			return false
		}
	}
	segments := decision.ReasonSegments()
	if strings.TrimSpace(decision.ReasonKey) != "" && len(segments) == 0 {
		return false
	}
	for _, segment := range segments {
		if !CurrentActionReviewAllowsSegment(segment) || !slices.Contains(review.ReasonSegments, segment) {
			return false
		}
	}
	if slices.Contains(gates, api.GateAgentPolicyChange) {
		if len(action.Mutations.AgentPolicy) == 0 {
			return false
		}
		for _, target := range action.Mutations.AgentPolicy {
			if !slices.Contains(review.AgentPolicy, target) {
				return false
			}
		}
	}
	if citation != nil || slices.Contains(gates, api.GateAuthorityMisuse) {
		if review.Detection == nil || citation == nil || !detectionIdentityEqual(*review.Detection, *citation) {
			return false
		}
	}
	for _, match := range matchedRules {
		if !slices.Contains(review.MatchedRules, match) {
			return false
		}
	}
	return true
}

func detectionIdentityEqual(a, b DetectionMatch) bool {
	return a.PackID == b.PackID &&
		a.RuleID == b.RuleID &&
		a.RuleTitle == b.RuleTitle &&
		a.Level == b.Level &&
		a.External == b.External &&
		a.Local == b.Local &&
		a.Unrecoverable == b.Unrecoverable &&
		a.Tagged == b.Tagged
}

// CurrentActionReviewAllowsFact reports whether a cited gate fact may be
// satisfied by a current-invocation human review receipt. Unobserved-channel
// facts are covered by their dedicated capability permits, except for the
// explicit LYCAON_SANDBOX=off unconfined subject.
func CurrentActionReviewAllowsFact(fact gate.Fact) bool {
	if fact.Gate != api.GateUnobservedChannel {
		return true
	}
	if !confine.SandboxDisabled() {
		return false
	}
	return (fact.Key == "boundary.applied" && fact.Value == "no") ||
		(fact.Key == "boundary.visibility" && fact.Value == "neither writes nor destinations are observed")
}

// CurrentActionReviewAllowsSegment reports whether a reason segment may be
// satisfied by a current-invocation human review receipt.
func CurrentActionReviewAllowsSegment(segment string) bool {
	prefix := string(api.GateUnobservedChannel) + ":"
	if !strings.HasPrefix(segment, prefix) {
		return true
	}
	return confine.SandboxDisabled() && segment == prefix+"unconfined"
}

type NativePathTarget struct {
	Path  string
	Write bool
}

// ActionScope binds an action to its project and session context.
type ActionScope struct {
	ProjectID          string
	ProjectDir         string
	SessionID          string
	RootSessionID      string
	SessionScratchRoot string
	SessionOutputRoot  string
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
	Contained                       Contained
	ProcessAccess                   string
	ProcessTargets                  []ApprovalTarget
	ExecutionBoundaryDigest         string
	AuthorizedExecutionCapabilities []string `json:"-"`
	PackageExecution                *packageexec.Execution
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
	// RecoveryLoss marks host-observed loss of undo for a prepared removal.
	RecoveryLoss bool `json:"recovery_loss,omitempty"`
	FileChanges  []api.ApprovalFileChange
	AgentPolicy  []AgentPolicyTarget
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
