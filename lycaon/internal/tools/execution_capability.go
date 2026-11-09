package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sync"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

// ToolContext copies share one permit, consumed at the launch boundary.
type executionPermit struct {
	mu                                             sync.Mutex
	session, call, action                          string
	boundary                                       string
	arguments                                      string
	processControl, credentialStore, hostExecution bool
	consumed                                       bool
}

type currentActionReview struct {
	mu           sync.Mutex
	session      string
	call         string
	action       string
	effectAction string
	boundary     string
	arguments    string
	review       hitl.CurrentActionReview
}

func invocationMatchesReview(
	tc ToolContext, args map[string]any, req confine.Request,
	session, call, action, boundary, arguments string,
) bool {
	if arguments != executionArgumentsDigest(args) {
		return false
	}
	if tc.Effects.CanonicalArgs != nil && arguments != executionArgumentsDigest(tc.Effects.CanonicalArgs) {
		return false
	}
	return boundary == ExecutionBoundaryDigest(req) &&
		session == tc.Identity.SessionID &&
		call == tc.Identity.ToolCallID &&
		action == tc.Invocation.ID
}

func (p *executionPermit) matchesLocked(tc ToolContext, args map[string]any, req confine.Request) bool {
	if p == nil || p.consumed {
		return false
	}
	if !invocationMatchesReview(tc, args, req, p.session, p.call, p.action, p.boundary, p.arguments) {
		return false
	}
	return p.hostExecution == req.HostExecution &&
		p.processControl == req.ProcessControl &&
		p.credentialStore == req.CredentialStore
}

func finalizeExecutionCapability(tc ToolContext, req confine.Request) *toolrejection.ToolReject {
	if !req.HostExecution && !req.ProcessControl && !req.CredentialStore {
		return nil
	}
	permit := tc.Execution.executionPermit
	if permit == nil {
		return &toolrejection.ToolReject{Code: isolation.CodeExecutionAuthorizationChanged}
	}
	permit.mu.Lock()
	defer permit.mu.Unlock()
	if !permit.matchesLocked(tc, tc.Effects.CanonicalArgs, req) {
		return &toolrejection.ToolReject{Code: isolation.CodeExecutionAuthorizationChanged}
	}
	permit.consumed = true
	return nil
}

// AuthorizedExecutionCapabilities returns the execution capabilities covered by
// an unconsumed current-call permit matching this invocation and boundary.
func (tc ToolContext) AuthorizedExecutionCapabilities(args map[string]any, req confine.Request) []string {
	if !req.HostExecution && !req.ProcessControl && !req.CredentialStore {
		return nil
	}
	permit := tc.Execution.executionPermit
	if permit == nil {
		return nil
	}
	permit.mu.Lock()
	defer permit.mu.Unlock()
	if !permit.matchesLocked(tc, args, req) {
		return nil
	}
	var out []string
	if permit.hostExecution {
		out = append(out, "host_execution")
	}
	if permit.processControl {
		out = append(out, "process_control")
	}
	if permit.credentialStore {
		out = append(out, "credential_store")
	}
	return out
}

// RecordCurrentActionReview binds the structured reasons approved by a human
// checkpoint to this invocation's identity, arguments, and confine boundary.
func (tc *ToolContext) RecordCurrentActionReview(
	args map[string]any, req confine.Request, action hitl.ProposedAction, result *hitl.ApprovalResult,
) {
	if tc == nil || result == nil || result.Decision == nil {
		return
	}
	actionKey := hitl.ActionKeyForCurrentReview(action)
	if actionKey == "" {
		return
	}
	rawSegments := result.Decision.ReasonSegments()
	allowUnconfined := confine.SandboxDisabled() &&
		slices.Contains(rawSegments, string(api.GateUnobservedChannel)+":unconfined")
	var cited []gate.Fact
	for _, fact := range result.Decision.Cited {
		if fact.Key == "boundary.execution" || (fact.Gate == api.GateUnobservedChannel && !allowUnconfined) {
			continue
		}
		if !slices.Contains(cited, fact) {
			cited = append(cited, fact)
		}
	}
	var segments []string
	for _, segment := range rawSegments {
		if !allowUnconfined && len(segment) > len(api.GateUnobservedChannel) &&
			segment[:len(api.GateUnobservedChannel)+1] == string(api.GateUnobservedChannel)+":" {
			continue
		}
		if !slices.Contains(segments, segment) {
			segments = append(segments, segment)
		}
	}
	var detection *hitl.DetectionMatch
	if result.DetectionCitation != nil {
		cp := *result.DetectionCitation
		cp.CorrelationID = ""
		detection = &cp
	}
	matchedRules := uniqueCompactSlice(nil, result.MatchedRules)
	var agentPolicy []hitl.AgentPolicyTarget
	if slices.Contains(result.Decision.Gates(), api.GateAgentPolicyChange) {
		agentPolicy = uniqueCompactSlice(nil, action.Mutations.AgentPolicy)
	}
	boundary, arguments, effectAction := ExecutionBoundaryDigest(req), executionArgumentsDigest(args), tc.EffectActionID()
	if existing := tc.Execution.currentActionReview; existing != nil {
		existing.mu.Lock()
		if existing.session == tc.Identity.SessionID && existing.call == tc.Identity.ToolCallID &&
			existing.action == tc.Invocation.ID && existing.effectAction == effectAction && existing.arguments == arguments {
			existing.boundary, existing.review.ActionKey = boundary, actionKey
			existing.review.Cited = uniqueCompactSlice(existing.review.Cited, cited)
			existing.review.ReasonSegments = uniqueCompactSlice(existing.review.ReasonSegments, segments)
			if detection != nil {
				existing.review.Detection = detection
			}
			existing.review.MatchedRules = uniqueCompactSlice(existing.review.MatchedRules, matchedRules)
			existing.review.AgentPolicy = uniqueCompactSlice(existing.review.AgentPolicy, agentPolicy)
			existing.mu.Unlock()
			return
		}
		existing.mu.Unlock()
	}
	tc.Execution.currentActionReview = &currentActionReview{
		session: tc.Identity.SessionID, call: tc.Identity.ToolCallID, action: tc.Invocation.ID,
		effectAction: effectAction, boundary: boundary, arguments: arguments,
		review: hitl.CurrentActionReview{
			SessionID: tc.Identity.SessionID, ToolCallID: tc.Identity.ToolCallID,
			InvocationID: tc.Invocation.ID, ActionID: effectAction, ActionKey: actionKey,
			Cited: cited, ReasonSegments: segments, Detection: detection,
			MatchedRules: matchedRules, AgentPolicy: agentPolicy,
		},
	}
}

func uniqueCompactSlice[T comparable](dst, src []T) []T {
	for _, item := range src {
		if !slices.Contains(dst, item) {
			dst = append(dst, item)
		}
	}
	return dst
}

// CurrentActionReview returns the current-call human review receipt when it
// matches this invocation's identity, arguments, and confine boundary.
func (tc ToolContext) CurrentActionReview(args map[string]any, req confine.Request) *hitl.CurrentActionReview {
	r := tc.Execution.currentActionReview
	if r == nil {
		return nil
	}
	if permit := tc.Execution.executionPermit; permit != nil {
		permit.mu.Lock()
		validPermit := permit.matchesLocked(tc, args, req)
		permit.mu.Unlock()
		if !validPermit {
			return nil
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !invocationMatchesReview(tc, args, req, r.session, r.call, r.action, r.boundary, r.arguments) ||
		r.effectAction != tc.EffectActionID() {
		return nil
	}
	out := r.review
	out.Cited = append([]gate.Fact(nil), r.review.Cited...)
	out.ReasonSegments = append([]string(nil), r.review.ReasonSegments...)
	if r.review.Detection != nil {
		cp := *r.review.Detection
		out.Detection = &cp
	}
	out.MatchedRules = append([]hitl.ApprovalRuleMatch(nil), r.review.MatchedRules...)
	out.AgentPolicy = append([]hitl.AgentPolicyTarget(nil), r.review.AgentPolicy...)
	return &out
}

func ExecutionBoundaryDigest(req confine.Request) string {
	// Set order and duplicates do not change the reviewed boundary.
	req.Roots = executionBoundarySet(req.Roots)
	req.GrantedWriteRoots = executionBoundarySet(req.GrantedWriteRoots)
	req.ReadRoots = executionBoundarySet(req.ReadRoots)
	req.ReadDenyPaths = executionBoundarySet(req.ReadDenyPaths)
	req.SocketGrants = executionBoundarySet(req.SocketGrants)
	req.ProtectedWriteGrants = executionBoundarySet(req.ProtectedWriteGrants)
	req.PolicyWriteGrants = executionBoundarySet(req.PolicyWriteGrants)
	req.ProtectedReadGrants = executionBoundarySet(req.ProtectedReadGrants)
	req.DirectIPDeclared = executionBoundarySet(req.DirectIPDeclared)
	req.LocalListenPorts = executionBoundarySet(req.LocalListenPorts)
	req.LoopbackConnectPorts = executionBoundarySet(req.LoopbackConnectPorts)
	raw, err := json.Marshal(req)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func executionArgumentsDigest(args map[string]any) string {
	raw, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Request set elements are strings, ports, or concrete path-grant records.
func executionBoundarySet[T comparable](values []T) []T {
	if len(values) == 0 {
		return nil
	}
	out := slices.Clone(values)
	slices.SortFunc(out, func(a, b T) int {
		left, _ := json.Marshal(a)
		right, _ := json.Marshal(b)
		return bytes.Compare(left, right)
	})
	return slices.Compact(out)
}

// StampExecutionApproval binds reviewed authority to this call and launch boundary.
func (tc *ToolContext) StampExecutionApproval(args map[string]any, reviewed hitl.ProposedAction) {
	tc.Execution.executionPermit = &executionPermit{session: tc.Identity.SessionID, call: tc.Identity.ToolCallID, action: tc.Invocation.ID, processControl: tc.Execution.ProcessControl, credentialStore: tc.Execution.CredentialStore, hostExecution: tc.Execution.HostExecution, boundary: reviewed.Execution.ExecutionBoundaryDigest, arguments: executionArgumentsDigest(args)}
}

// HasExecutionApproval reports whether this invocation reached capability review.
func (tc ToolContext) HasExecutionApproval() bool { return tc.Execution.executionPermit != nil }
