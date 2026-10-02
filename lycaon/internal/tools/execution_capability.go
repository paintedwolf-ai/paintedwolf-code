package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sync"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
)

// ToolContext copies share one permit, consumed at the launch boundary.
type executionPermit struct {
	mu                            sync.Mutex
	session, call                 string
	boundary                      string
	arguments                     string
	processControl, hostExecution bool
	consumed                      bool
}

func (e *DefaultToolExecutor) preflightExecutionCapability(ctx context.Context, tool string, args map[string]any, tc *ToolContext) error {
	request, reject := ParseCapabilityRequest(args)
	if reject != nil {
		return reject
	}
	if request == nil || (!request.ProcessControl && !request.HostExecution) {
		return nil
	}
	tc.ProcessControl, tc.HostExecution = request.ProcessControl, request.HostExecution
	action := e.executionCapabilityAction(ctx, tool, args, *tc)
	if request.ProcessControl && !action.Contained.FSJailed {
		return &ToolReject{Code: isolation.CodeExecutionBoundaryUnavailable}
	}
	result, err := e.evaluatePreSpawn(ctx, action)
	if err != nil {
		return err
	}
	if result == nil {
		return &ToolReject{Code: isolation.CodeApprovalUnavailable}
	}
	if result.Denied {
		return e.rejectBoundaryPolicyDeny(ctx, tool, args, *tc, result)
	}
	if result.Required() {
		if err := e.awaitExecutionCapability(ctx, action, *tc, result); err != nil {
			return err
		}
	}
	tc.executionPermit = &executionPermit{session: tc.SessionID, call: tc.ToolCallID, processControl: tc.ProcessControl, hostExecution: tc.HostExecution, boundary: action.ExecutionBoundaryDigest, arguments: executionArgumentsDigest(args)}
	return nil
}

func (e *DefaultToolExecutor) executionCapabilityAction(ctx context.Context, tool string, args map[string]any, tc ToolContext) hitl.ProposedAction {
	request := e.actionConfineRequest(ctx, tc)
	action := proposedActionFromPolicy(e.preInvokePolicyContext(tool, tc.ProfileID(), args, tc, request))
	action.ExecutionBoundaryDigest = executionBoundaryDigest(request)
	action.Command = commandsurface.PrimaryCommandLine(args, nil)
	return action
}

func (e *DefaultToolExecutor) awaitExecutionCapability(ctx context.Context, action hitl.ProposedAction, tc ToolContext, result *hitl.ApprovalResult) error {
	subject, title, impact, consequence := executionCapabilityCopy(tc.HostExecution)
	targets := []hitl.ApprovalTarget{{Kind: string(subject), Label: action.Command}}
	who := hitl.WhoAgentCommand
	if action.ProcessAccess != "" {
		subject = hitl.ApprovalSubjectAction
		who = hitl.WhoAgentAction
		title, impact, consequence = hitl.ProcessSignalTitle, hitl.ProcessSignalWhat, hitl.ProcessSignalIfWrong
		targets = action.ProcessTargets
		if action.ProcessAccess == "list" {
			title, impact, consequence = hitl.ProcessListTitle, hitl.ProcessListWhat, hitl.ProcessListIfWrong
		} else {
			subject = hitl.ApprovalSubjectActionSet
		}
	}
	options := []hitl.ApprovalOption{hitl.CurrentActionOption()}
	offers := e.grantOffers(action, result)
	for _, offer := range offers {
		options = append(options, hitl.GrantOption(offer))
	}
	options = append(options, e.quietOptionsFor(action, result.Decision)...)
	primary, cited, reasons := hitl.PresentDecision(result.Decision)
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: subject, Title: title, Targets: targets,
	}, hitl.ApprovalPresentation{
		Action: title, Tool: action.Tool, Command: action.Command,
		Impact: impact, Who: who, IfWrong: consequence,
		AllowLine: hitl.ExecutionAllowLine,
		Gate:      primary, Cited: cited, Detection: detectionOf(result), GrantDelta: result.GrantDelta,
	}, reasons, options, hitl.FaceContext{})
	if err != nil {
		return ApprovalPlanInvalid()
	}
	permission, err := e.prepareSecretPermission(ctx, action.Tool, action.Args, tc)
	if err != nil {
		return err
	}
	plan, err = hitl.ComposeSecretPermission(plan, action, permission, hitl.FaceContext{})
	if err != nil {
		return ApprovalPlanInvalid()
	}
	final, err := e.raiseAndWaitToolApproval(ctx, toolApprovalRaise{
		Action: action, Plan: plan, Title: title, ToolCallID: tc.ToolCallID, ProjectID: tc.ProjectID,
		Decision: result.Decision, Detection: detectionOf(result), ApprovalMatches: approvalRuleMatches(result),
		SkipGrantOfferAutofill: true, SecretScreenHit: permission != nil,
		CoalesceKey: permission.Key(hitl.GrantKey(action)),
		GrantOffers: offers,
	})
	if err != nil {
		return err
	}
	if !hitl.CheckpointAuthorizes(final) {
		return isolationCheckpointReject(isolation.CodeExecutionCapabilityDenied, final)
	}
	approveCapabilitySecretPermission(ctx, tc, permission, attestationOf(final))
	return nil
}

func executionCapabilityCopy(host bool) (hitl.ApprovalSubjectKind, string, string, string) {
	if host {
		return hitl.ApprovalSubjectHostExecution, hitl.HostExecutionTitle, hitl.HostExecutionWhat, hitl.HostExecutionIfWrong
	}
	return hitl.ApprovalSubjectProcessControl, hitl.ProcessControlTitle, hitl.ProcessControlWhat, hitl.ProcessControlIfWrong
}

func finalizeExecutionCapability(tc ToolContext, req confine.Request) *ToolReject {
	if !req.HostExecution && !req.ProcessControl {
		return nil
	}
	permit := tc.executionPermit
	if permit == nil {
		return &ToolReject{Code: isolation.CodeExecutionAuthorizationChanged}
	}
	permit.mu.Lock()
	defer permit.mu.Unlock()
	if permit.arguments != executionArgumentsDigest(tc.CanonicalArgs) || permit.boundary != executionBoundaryDigest(req) || permit.consumed || permit.session != tc.SessionID || permit.call != tc.ToolCallID || permit.hostExecution != req.HostExecution || permit.processControl != req.ProcessControl {
		return &ToolReject{Code: isolation.CodeExecutionAuthorizationChanged}
	}
	permit.consumed = true
	return nil
}

func executionBoundaryDigest(req confine.Request) string {
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
