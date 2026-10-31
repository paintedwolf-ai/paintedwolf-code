package toolexecution

import (
	"github.com/lycaon/lycaon/internal/capabilitygrants"

	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// socketCapabilityPreflightResult is the executor-facing outcome of socket preflight.
type socketCapabilityPreflightResult struct {
	SocketGrants            []confine.SocketGrant
	DurableSocketGrants     []confine.SocketGrant
	AuthorizedSocketDigests []string
	ActionDigest            string
	AuthorizationSource     string
	ApprovalSatisfied       bool
	SocketScopes            []string
	SocketGrantStates       []string
}

// SetSocketCapabilityRuntime wires AF_UNIX chat grants and current-call permits.
func (e *Capabilities) SetSocketCapabilityRuntime(rt tools.SocketCapabilityRuntime) {
	if e != nil {
		e.socketRuntime = rt
	}
}

// SetDurableSocketSource wires project/device exact AF_UNIX grants into preflight.
func (e *Capabilities) SetDurableSocketSource(fn tools.DurableSocketSource) {
	if e != nil {
		e.durableSockets = fn
	}
}

// SetApprovalsDisabled wires the socket-card override.
func (e *Capabilities) SetApprovalsDisabled(fn func(projectDir string) bool) {
	if e != nil {
		e.approvalsDisabled = fn
	}
}

func (e *Capabilities) preflightSocketCapability(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc tools.ToolContext,
) (*socketCapabilityPreflightResult, error) {
	contract := tc.Invocation.Contract
	if !contract.Supports(toolcontract.CapabilitySocket) && contract.SocketArg == "" {
		return nil, nil
	}
	capReq, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, reject)
	}
	if capReq == nil {
		capReq = &capabilityrequest.CapabilityRequest{}
	}
	// Held sessions cannot outlive one-action direct-IP review.
	if capReq.DirectIP != nil && !contract.Supports(toolcontract.CapabilityDirectIP) {
		return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{
			Code: isolation.CodeDirectIPRequestInvalid,
			Data: map[string]any{
				"reason":                     "a held session cannot hold one-action direct network authority",
				"successor":                  "command",
				"direct_ip_held_unsupported": true,
			},
		})
	}
	socketPaths := requestedSocketPaths(tc, args, capReq)
	if len(socketPaths) == 0 {
		return nil, nil
	}
	if tc.Files.PackageExecution != nil {
		return nil, nil
	}
	adapter := socketref.Platform()
	if e.socketAdapter != nil {
		adapter = *e.socketAdapter
	}
	if contract.SocketArg != "" && adapter.Native != socketref.RetainedObject {
		return nil, e.refuseUnsupportedNativeSocket(ctx, tool, args, tc, socketPaths, adapter.Native)
	}
	chatSession := tc.ChatSessionID()
	chatGrants := []confine.SocketGrant{}
	if e.socketRuntime != nil {
		chatGrants = e.socketRuntime.AppliedGrants(chatSession)
	}
	durableGrants := e.durableSocketGrants(tc)
	requested, reject := capabilityrequest.ResolveCapabilitySockets(&capabilityrequest.CapabilityRequest{SocketPaths: socketPaths})
	if reject != nil {
		return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, reject)
	}
	// Capture once before review. Repeated preflight in the same invocation
	// retains that object rather than looking up a replacement pathname.
	for i, grant := range requested {
		if contract.SocketArg != "" {
			grant.Binding.Enforcement = socketref.RetainedObject
		}
		bound, err := confine.CaptureSocketGrant(ctx, grant)
		if err != nil {
			return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, capabilityrequest.SocketResolveReject(err))
		}
		requested[i] = bound
	}
	// Existing authority can be reused only for the same retained object and
	// enforcement contract. Pathname grants cannot mint native object authority.
	saved := append(append([]confine.SocketGrant(nil), chatGrants...), durableGrants...)
	for i, grant := range requested {
		if grant.Binding.Mode() != socketref.RetainedObject {
			continue
		}
		for _, prior := range saved {
			if prior.ApprovedPath != grant.ApprovedPath || prior.ResolvedPath != grant.ResolvedPath || prior.Binding.Mode() != grant.Binding.Mode() {
				continue
			}
			if socketref.ReuseForInvocation(ctx, grant.ApprovedPath, prior.Binding) {
				requested[i].Binding = prior.Binding
				break
			}
		}
	}
	overlay := mergeAuthorizedWithOverlay(nil, requested, saved)
	e.recordCapabilityRequested(ctx, tc, tool, requested)
	merged := requested
	confInputs := tools.ActionConfineInputsForContext(tc, e.Boundary.overlayWriteRoots(ctx, tc))
	confInputs.SocketGrants = merged
	if capReq.DirectIP != nil {
		// The gate and executor use the same narrowing.
		confInputs.DirectIP = true
		confInputs.DirectIPDeclared = capReq.DirectIP.DeclaredDestinations
	}
	confReq := hitl.ActionConfineRequest(confInputs)
	action := socketCapabilityProposedAction(tool, args, tc, chatSession, merged, confReq)
	actionDigest := hitl.GrantKey(action)
	if actionDigest == "" {
		return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, toolrejection.ApprovalPlanInvalid())
	}
	authorized := []confine.SocketGrant{}
	if e.socketRuntime != nil {
		authorized = e.socketRuntime.AuthorizedGrants(chatSession, tc.PermitIdentity(), actionDigest, requested)
	}
	authorized = mergeAuthorizedWithOverlay(authorized, requested, overlay)
	observation := socketObservationFields(merged, requested, chatGrants, durableGrants, authorized)
	action.Sockets.SocketScopes = append([]string(nil), observation.scopes...)
	action.Sockets.SocketGrantStates = append([]string(nil), observation.states...)
	chatByPair := capabilitygrants.SocketGrantPairSet(overlay)
	authSource := ""
	if len(authorized) > 0 {
		authSource = authzledger.AuthorizationSourceHuman
	}
	if len(overlay) == len(requested) {
		authSource = authzledger.AuthorizationSourceLease
	}
	// Existing route authority suppresses only the socket card.
	approvalSatisfied := false
	missing := make([]confine.SocketGrant, 0, len(requested))
	for _, grant := range requested {
		if capabilitygrants.GrantAuthorized(grant, authorized) {
			continue
		}
		if e.approvalsDisabled != nil && e.approvalsDisabled(tc.ActiveRootPath()) {
			if e.socketRuntime != nil {
				e.socketRuntime.IssuePermit(tc.ChatSessionID(), tc.PermitIdentity(), actionDigest, grant)
			}
			authSource = authzledger.AuthorizationSourceNeverAsk
			e.recordCapabilityDecision(ctx, tc, tool, grant, true, authSource)
			continue
		}
		missing = append(missing, grant)
	}
	if len(missing) > 0 {
		source, err := e.authorizeMissingSocketGrants(
			ctx, tool, args, tc, capReq, action, actionDigest, missing, chatByPair,
			merged, durableGrants, observation,
		)
		if err != nil {
			return nil, err
		}
		approvalSatisfied = true
		authSource = source
	}
	if e.socketRuntime != nil {
		authorized = e.socketRuntime.AuthorizedGrants(chatSession, tc.PermitIdentity(), actionDigest, requested)
	}
	authorized = mergeAuthorizedWithOverlay(authorized, requested, overlay)
	for _, grant := range requested {
		if !capabilitygrants.GrantAuthorized(grant, authorized) {
			return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{
				Code: isolation.CodeSocketPathChanged,
				Data: map[string]any{"reason": "approval did not install current-call socket authority"},
			})
		}
	}
	return &socketCapabilityPreflightResult{
		SocketGrants:            merged,
		DurableSocketGrants:     append([]confine.SocketGrant(nil), durableGrants...),
		AuthorizedSocketDigests: capabilitygrants.SocketGrantDigests(merged),
		ActionDigest:            actionDigest,
		AuthorizationSource:     authSource,
		ApprovalSatisfied:       approvalSatisfied || authSource == authzledger.AuthorizationSourceNeverAsk,
		SocketScopes:            observation.scopes,
		SocketGrantStates:       observation.states,
	}, nil
}

// requestedSocketPaths joins capability_request.socket_paths with the socket a
// tool's declared socket argument names; both are the same exact authority.
func requestedSocketPaths(tc tools.ToolContext, args map[string]any, capReq *capabilityrequest.CapabilityRequest) []string {
	var paths []string
	if tc.Invocation.Contract.Supports(toolcontract.CapabilitySocket) {
		paths = append(paths, capReq.SocketPaths...)
	}
	if arg := tc.Invocation.Contract.SocketArg; arg != "" {
		if raw, _ := args[arg].(string); strings.TrimSpace(raw) != "" {
			paths = append(paths, tools.SocketArgPath(tc, raw))
		}
	}
	return paths
}

// SocketArgPath is the path a declared socket argument names, joined to the
// active root when relative. Socket resolution refuses what stays relative.

func socketCapabilityProposedAction(
	tool string,
	args map[string]any,
	tc tools.ToolContext,
	chatSession string,
	merged []confine.SocketGrant,
	confReq confine.Request,
) hitl.ProposedAction {
	contained := hitl.ContainedForRequest(confReq)
	if tc.Invocation.Contract.SocketArg != "" {
		contained = contained.WithDialedSockets(merged)
	}
	return hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			NativeInputs:  tc.Invocation.NativeInputs,
			Tool:          tool,
			Args:          args,
			Files:         filesFromArgs(tool, args),
			ResolvedFiles: ResolvedApprovalFiles(tool, args, tc),
			ActionID:      tc.EffectActionID(),
		},
		Presentation: hitl.ActionPresentation{
			Command: commandsurface.PrimaryCommandLine(args, nil),
		},
		Scope: hitl.ActionScope{
			ProjectID:          tc.Identity.ProjectID,
			ProjectDir:         tc.ActiveRootPath(),
			SessionID:          tc.Identity.SessionID,
			RootSessionID:      chatSession,
			SessionScratchRoot: tc.Host.SessionScratchDir,
			SessionOutputRoot:  tc.Host.SessionOutputDir,
		},
		Sockets: hitl.ActionSockets{
			SocketGrants: append([]confine.SocketGrant(nil), merged...),
		},
		Execution: hitl.ActionExecution{
			Contained: contained,
		},
		Egress: hitl.ActionEgress{
			Visibility: "unobserved",
		},
		Resources: hitl.ActionResources{
			HostResources:        append([]string(nil), tc.Host.HostResources...),
			HostResourceFamilies: append([]string(nil), tc.Host.HostResourceFamilies...),
		},
	}
}

// authorizeMissingSocketGrants reviews grants absent from the action digest.
func (e *Capabilities) authorizeMissingSocketGrants(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc tools.ToolContext,
	capReq *capabilityrequest.CapabilityRequest,
	action hitl.ProposedAction,
	actionDigest string,
	missing []confine.SocketGrant,
	chatByPair map[string]struct{},
	merged []confine.SocketGrant,
	durableGrants []confine.SocketGrant,
	observation socketObservation,
) (authSource string, err error) {
	approval, evalErr := e.Approvals.evaluatePreSpawn(ctx, action)
	if evalErr != nil {
		return "", evalErr
	}
	if approval != nil && approval.Denied {
		return "", e.Approvals.rejectBoundaryPolicyDeny(ctx, tool, args, tc, approval)
	}
	var direct *directIPApprovalReview
	if capReq.DirectIP != nil {
		predicted := tc
		predicted.Socket.SocketGrants = append([]confine.SocketGrant(nil), merged...)
		predicted.Socket.DurableSocketGrants = append([]confine.SocketGrant(nil), durableGrants...)
		predicted.Socket.AuthorizedSocketDigests = capabilitygrants.SocketGrantDigests(merged)
		predicted.Socket.SocketActionDigest = actionDigest
		predicted.Socket.SocketScopes = append([]string(nil), observation.scopes...)
		predicted.Socket.SocketGrantStates = append([]string(nil), observation.states...)
		review := e.buildDirectIPApprovalReview(ctx, tool, args, predicted, capReq.DirectIP.DeclaredDestinations)
		if e.directIPRuntime == nil || !e.directIPRuntime.Authorized(tc.PermitIdentity(), review.Lease.ActionDigest) {
			if !e.directIPLeaseAuthorizes(review.Action, predicted, review.Lease) {
				direct = &review
			}
		}
	}
	if direct == nil {
		if covered, extras := e.permitCoveredHostResourceSockets(ctx, action, actionDigest, missing, tc); covered {
			if len(extras) == 0 {
				return authzledger.AuthorizationSourceLease, nil
			}
			missing = extras
		}
	}
	if err := e.awaitSocketCapabilities(ctx, action, actionDigest, missing, chatByPair, tc, approval, direct); err != nil {
		if hitl.IsPreparedApproval(err) {
			return "", err
		}
		for _, grant := range missing {
			e.recordCapabilityDecision(ctx, tc, tool, grant, false, authzledger.AuthorizationSourceHuman)
		}
		return "", e.Approvals.rejectApprovalErr(ctx, tool, tc.Identity.Agent, args, err)
	}
	authSource = authzledger.AuthorizationSourceHuman
	for _, grant := range missing {
		e.recordCapabilityDecision(ctx, tc, tool, grant, true, authSource)
	}
	return authSource, nil
}

type socketObservation struct {
	scopes []string
	states []string
}

func socketObservationFields(
	grants, requested, chat, durable, authorized []confine.SocketGrant,
) socketObservation {
	requestedPairs := capabilitygrants.SocketGrantPairSet(requested)
	chatPairs := capabilitygrants.SocketGrantPairSet(chat)
	durablePairs := capabilitygrants.SocketGrantPairSet(durable)
	authorizedPairs := capabilitygrants.SocketGrantPairSet(authorized)
	result := socketObservation{}
	for _, grant := range grants {
		key := capabilitygrants.SocketGrantPairKey(grant)
		var scope string
		state := "existing"
		switch {
		case hasSocketPair(durablePairs, key):
			scope = "durable"
		case hasSocketPair(chatPairs, key):
			scope = "chat"
		case hasSocketPair(requestedPairs, key) && hasSocketPair(authorizedPairs, key):
			scope = "current_action"
		case hasSocketPair(requestedPairs, key):
			scope, state = "requested", "new"
		default:
			continue
		}
		result.scopes = append(result.scopes, scope)
		result.states = append(result.states, state)
	}
	return result
}

func hasSocketPair(pairs map[string]struct{}, key string) bool {
	_, ok := pairs[key]
	return ok
}

// Native consumers require retained-object realization before requesting approval.
func (e *Capabilities) refuseUnsupportedNativeSocket(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext, socketPaths []string, enforcement socketref.Enforcement) error {
	denied := make([]confine.SocketGrant, 0, len(socketPaths))
	for _, path := range socketPaths {
		denied = append(denied, confine.SocketGrant{ApprovedPath: path})
	}
	e.recordCapabilityRequested(ctx, tc, tool, denied)
	for _, grant := range denied {
		e.recordCapabilityDecision(ctx, tc, tool, grant, false, authzledger.AuthorizationSourcePolicy)
	}
	return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{Code: isolation.CodeSocketPathRefused, Data: map[string]any{"path": socketPaths[0], "state": "socket_realization_unavailable", "enforcement": string(enforcement), "recovery": "use_approved_service_endpoint", "reason": socketref.ErrUnsupported.Error()}})
}
