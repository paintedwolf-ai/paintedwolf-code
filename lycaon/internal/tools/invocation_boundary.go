package tools

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// applyPreInvokeBoundary prepares and reviews confinement.
func (e *DefaultToolExecutor) applyPreInvokeBoundary(
	ctx context.Context,
	tool, profileID string,
	args map[string]any,
	tc ToolContext,
) (map[string]any, ToolContext, error) {
	var err error
	// Stamp SOCKS injection before building confinement requests.
	if socks, reject := parseSocksProxyArg(args); reject != nil {
		return nil, tc, e.rejectBeforeInvoke(ctx, tool, profileID, args, reject)
	} else if socks {
		tc.SocksProxyEnv = true
	}
	var hostResourceResolution *hostresources.ActionResolution
	args, hostResourceResolution, err = e.expandHostResourceConnections(ctx, tool, args, tc)
	if err != nil {
		return nil, tc, err
	}
	if hostResourceResolution != nil {
		tc.HostResources = sortedHostResourceIDs(hostResourceResolution.States)
		tc.HostResourceFamilies = append([]string(nil), hostResourceResolution.Families...)
		tc.HostResourceAsk = append([]string(nil), hostResourceResolution.Ask...)
		tc.HostResourcePathExtra = append([]string(nil), hostResourceResolution.PathExtra...)
		tc.RealizationWriteRoots = append([]string(nil), hostResourceResolution.WriteRoots...)
		tc.RealizationSockets = realizationSocketTargets(hostResourceResolution)
	}
	if err := e.preflightPackageExecution(ctx, tool, profileID, args, &tc); err != nil {
		return nil, tc, err
	}
	if err := e.rejectPackageBoundaryWidening(ctx, tool, profileID, args, tc); err != nil {
		return nil, tc, err
	}
	// A call that runs on the worker's private branch claims it before anything
	// reads the tree, so expansion and every review see where it runs.
	tc, err = e.prepareWorkerBranch(ctx, tool, profileID, args, tc)
	if err != nil {
		return nil, tc, err
	}
	// Scratch addresses and globs expand once, before secret resolution and
	// every card, so each card and the executed argv carry the same arguments.
	args, err = e.expandCommandOperands(ctx, tool, profileID, args, &tc)
	if err != nil {
		return nil, tc, err
	}
	// Capture one immutable version for both review and execution; cards retain references.
	secrets, resolveErr := e.resolveSecretReferences(ctx, tool, args, tc)
	if resolveErr != nil {
		return nil, tc, e.rejectBeforeInvoke(ctx, tool, profileID, args, secretReferenceReject(resolveErr))
	}
	tc.Secrets = secrets
	ctx = secretcap.WithResolution(ctx, secrets)
	if tc.Invocation.Contract.SecretReferenceSurface.IsFile() && tool != "jq_edit" && secrets != nil && secrets.HasUnsafeFileBytes() {
		return nil, tc, e.rejectBeforeInvoke(ctx, tool, profileID, args, &ToolReject{
			Code: "SECRET_REFERENCE_VALUE_UNSAFE",
			Data: map[string]any{"tool": tool},
		})
	}
	request, reject := ParseCapabilityRequest(args)
	if reject != nil {
		return nil, tc, reject
	}
	if request != nil {
		tc.ProcessControl, tc.HostExecution = request.ProcessControl, request.HostExecution
	}
	ctx, err = e.reviewInvocationCapabilities(ctx, tool, args, tc)
	if err != nil {
		return nil, tc, err
	}
	if err := e.preflightWriteRoot(ctx, tool, args, &tc); err != nil {
		return nil, tc, err
	}
	if err := e.preflightReadPath(ctx, tool, args, &tc); err != nil {
		return nil, tc, err
	}
	if e.sessionReadOverlay != nil {
		tc.SessionReadPaths = append([]string(nil), e.sessionReadOverlay(ctx, tc.SessionID, tc.ParentSessionID)...)
	}
	socketPreflight, err := e.preflightSocketCapability(ctx, tool, args, tc)
	if err != nil {
		return nil, tc, err
	}
	if socketPreflight != nil {
		tc.SocketGrants = append([]confine.SocketGrant(nil), socketPreflight.SocketGrants...)
		tc.DurableSocketGrants = append([]confine.SocketGrant(nil), socketPreflight.DurableSocketGrants...)
		tc.AuthorizedSocketDigests = append([]string(nil), socketPreflight.AuthorizedSocketDigests...)
		tc.SocketActionDigest = socketPreflight.ActionDigest
		tc.SocketCapabilityRuntime = e.socketRuntime
		tc.SocketAuthorizationSource = socketPreflight.AuthorizationSource
		tc.SocketScopes = append([]string(nil), socketPreflight.SocketScopes...)
		tc.SocketGrantStates = append([]string(nil), socketPreflight.SocketGrantStates...)
	}
	tc.AuthzRecorder = e.authzRecorder
	directPreflight, err := e.preflightDirectIPCapability(
		ctx,
		tool,
		args,
		tc,
		socketPreflight != nil && socketPreflight.ApprovalSatisfied,
	)
	if err != nil {
		return nil, tc, err
	}
	if directPreflight != nil {
		tc.DirectIPRequested = directPreflight.Requested
		tc.DirectIPDeclared = append([]string(nil), directPreflight.Declared...)
		tc.DirectIPActionDigest = directPreflight.ActionDigest
		tc.DirectIPRequestDigest = directPreflight.RequestDigest
		tc.DirectIPConfineDigest = directPreflight.ConfineDigest
		tc.DirectIPAuthorized = directPreflight.Authorized
		tc.DirectIPCapabilityRuntime = e.directIPRuntime
		tc.DirectIPLifecycle = e.directIPLifecycle
		tc.DirectIPBackground = boolArg(args, "background")
	}
	// Live leases first, then combined or single-axis preflight.
	e.applySessionListenGrant(ctx, &tc)
	e.applySessionLoopbackGrant(ctx, &tc)
	listenPreflight, connectPreflight, err := e.preflightLocalNetworkCapability(ctx, tool, args, tc)
	if err != nil {
		return nil, tc, err
	}
	if listenPreflight != nil && listenPreflight.Authorized {
		tc.LocalListenGranted = true
		tc.LocalListenPorts = append([]uint16(nil), listenPreflight.Ports...)
	}
	if connectPreflight != nil && connectPreflight.Authorized {
		tc.LoopbackConnectGranted = true
		tc.LoopbackConnectPorts = append([]uint16(nil), connectPreflight.Ports...)
	}
	if err := e.preflightExecutionCapability(ctx, tool, args, &tc); err != nil {
		return nil, tc, err
	}
	if permission, ok := ctx.Value(capabilityReviewSecretKey{}).(*hitl.SecretPermission); ok {
		approveSecretPermission(tc.Secrets, permission)
	}
	// Policy, native path resolution, and execution share one request.
	confReq := e.actionConfineRequest(ctx, tc)
	tc.GrantedWriteRoots = append([]string(nil), confReq.GrantedWriteRoots...)
	boundaryApproved := (socketPreflight != nil && socketPreflight.ApprovalSatisfied) ||
		(directPreflight != nil && directPreflight.ApprovalSatisfied) || tc.executionPermit != nil
	// Socket/direct outer actions reach profile policy.Evaluate with BoundaryApprovalSatisfied via applyPreInvokePolicy.
	args, err = e.applyPreInvokePolicy(ctx, tool, profileID, args, &tc, confReq, boundaryApproved)
	if err != nil {
		return nil, tc, err
	}
	return args, tc, nil
}

// applyPreInvokePolicy evaluates policy constraints, file access checkpoints, and human approval.
func (e *DefaultToolExecutor) applyPreInvokePolicy(
	ctx context.Context,
	tool, profileID string,
	args map[string]any,
	tc *ToolContext,
	confReq confine.Request,
	boundaryApprovalSatisfied bool,
) (map[string]any, error) {
	if e.policy == nil {
		return args, nil
	}
	policyEval := e.preInvokePolicyContext(tool, profileID, args, *tc, confReq)
	policyEval.BoundaryApprovalSatisfied = boundaryApprovalSatisfied
	policyEval.PolicyWritesReviewed = tc.executionPermit != nil
	decision, err := e.policy.Evaluate(ctx, policyEval)
	if err != nil {
		return nil, err
	}
	if decision.Blocked {
		e.recordToolDenied(ctx, policyEval, decision, decision.Approval)
		return nil, e.rejectBeforeInvoke(ctx, tool, profileID, args, policyBlockReject(decision, tool, profileID))
	}
	if len(tc.PolicyWriteGrants) != 0 && (e.approvalGate == nil || e.checkpointMgr == nil) {
		return nil, fmt.Errorf("agent policy write requires the approval gate")
	}
	if tc.Invocation.Contract.Supports(toolcontract.CapabilityFileChange) {
		if decision.RequiresApproval && (e.approvalGate == nil || e.checkpointMgr == nil) {
			return nil, fmt.Errorf("file change approval checkpoints not configured")
		}
		if decision.Approval != nil {
			tc.PreparedFileAccess = append([]hitl.GrantedPathDelta(nil), decision.Approval.FileAccess...)
		}
		return args, nil
	}
	// Process operations review their resolved instances inside the owner.
	if tool == "process_list" || tool == "process_signal" {
		return args, nil
	}
	if decision.RequiresApproval {
		if e.checkpointMgr == nil {
			return nil, fmt.Errorf("tool approval checkpoints not configured")
		}
		runArgs, err := e.awaitApproval(ctx, tool, args, *tc, decision.Approval, confReq)
		if err != nil {
			return nil, err
		}
		args = runArgs
		if decision.Approval != nil {
			tc.ApprovedFileAccess = append([]hitl.GrantedPathDelta(nil), decision.Approval.FileAccess...)
		}
	}
	return args, nil
}
