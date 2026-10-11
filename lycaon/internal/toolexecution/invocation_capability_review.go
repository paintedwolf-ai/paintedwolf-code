package toolexecution

import (
	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"slices"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/pkg/api"
)

type capabilityReviewRootsKey struct{}
type capabilityReviewSecretKey struct{}

// Secret targets remain available until every capability replays the held subject.
func approveCapabilitySecretPermission(ctx context.Context, tc tools.ToolContext, permission *hitl.SecretPermission) {
	if !hitl.HasPreparedApprovalAnswers(ctx) {
		approveSecretPermission(tc.Effects.Secrets, permission)
	}
}

// Reviews prepare together; execution preflight uses committed grants and answers.
func (e *Executor) reviewInvocationCapabilities(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext) (context.Context, error) {
	request, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		return ctx, e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, reject)
	}
	if !multipleCapabilityFamilies(request) {
		return ctx, nil
	}
	permission, err := e.Secrets.prepareSecretPermission(ctx, tool, args, tc)
	if err != nil {
		return ctx, err
	}
	reviews, err := e.prepareInvocationCapabilities(hitl.WithApprovalPreparation(ctx), tool, args, tc, request)
	if err != nil || len(reviews) == 0 {
		return ctx, err
	}
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: tool,
Args: args,
ActionID: tc.Identity.ToolCallID,
},
Presentation: hitl.ActionPresentation{
Command: commandsurface.PrimaryCommandLine(args, nil),
},
Scope: hitl.ActionScope{
ProjectID: tc.Identity.ProjectID,
ProjectDir: tc.ActiveRootPath(),
SessionID: tc.Identity.SessionID,
RootSessionID: tc.ChatSessionID(),
},
Execution: hitl.ActionExecution{
Contained: hitl.ContainedForRequest(e.Boundary.actionConfineRequest(ctx, tc)),
},
Resources: hitl.ActionResources{
HostResources: append([]string(nil), tc.Host.HostResources...),
HostResourceFamilies: append([]string(nil), tc.Host.HostResourceFamilies...),
},
}
	plan, decision, err := hitl.ComposeCapabilityApprovals(action, reviews)
	if len(reviews) == 1 {
		action = *reviews[0].Request.ProposedAction
		plan, decision, err = reviews[0].Request.ApprovalPlan, reviews[0].Request.Decision, nil
	}
	if err != nil {
		if errors.Is(err, hitl.ErrNoCommonApprovalDuration) {
			return ctx, nil
		}
		return ctx, e.Approvals.rejectApprovalErr(ctx, tool, tc.Identity.Agent, args, toolrejection.ApprovalPlanInvalid())
	}
	secretIncluded := slices.ContainsFunc(plan.Subject.Targets, func(target hitl.ApprovalTarget) bool { return target.Kind == "secret" })
	final, err := e.Approvals.raiseAndWaitToolApproval(ctx, toolApprovalRaise{
		Action: action, Plan: plan, Decision: decision, Title: plan.Subject.Title,
		ToolCallID: tc.Identity.ToolCallID, ProjectID: tc.Identity.ProjectID,
		SkipGrantOfferAutofill: true,
		ApprovalMatches:        plan.Presentation.ApprovalRules, Detection: plan.Presentation.Detection,
		ConsequenceBand: api.ConsequenceBand(plan.Presentation.ConsequenceBand),
		ConsequenceCode: api.ConsequenceCode(plan.Presentation.ConsequenceCode),
		CoalesceKey:     hitl.CapabilityApprovalKey(action, plan),
		SecretScreenHit: secretIncluded,
	})
	if err != nil {
		return ctx, err
	}
	ctx = hitl.WithPreparedApprovalAnswer(ctx, reviews, final)
	if !hitl.CheckpointAuthorizes(final) {
		return ctx, e.Approvals.rejectApprovalErr(ctx, tool, tc.Identity.Agent, args, isolationCheckpointReject(capabilityReviewDenialCode(plan), final))
	}
	if secretIncluded {
		ctx = context.WithValue(ctx, capabilityReviewSecretKey{}, permission)
	}
	return ctx, nil
}

func capabilityReviewDenialCode(plan *hitl.ApprovalPlan) string {
	for _, target := range plan.Subject.Targets {
		switch target.Kind {
		case "write_root", "credential_file", "key_material":
			return isolation.CodeWriteRootDenied
		case "read_path":
			return isolation.CodeReadPathDenied
		case "socket":
			return isolation.CodeSocketPathDenied
		case "process_control", "host_execution":
			return isolation.CodeExecutionCapabilityDenied
		case "direct_ip":
			return isolation.CodeDirectIPDenied
		case "local_listen", "loopback_connect":
			return isolation.CodeLocalNetworkDenied
		}
	}
	return isolation.CodeApprovalUnavailable
}

func multipleCapabilityFamilies(request *capabilityrequest.CapabilityRequest) bool {
	if request == nil {
		return false
	}
	count := 0
	for _, present := range []bool{
		request.WriteRoot != "", request.ReadPath != "", len(request.SocketPaths) > 0,
		request.ProcessControl || request.HostExecution, request.DirectIP != nil, request.LocalListen != nil || request.LoopbackConnect != nil,
	} {
		if present {
			count++
		}
	}
	return count > 1
}

func (e *Executor) prepareInvocationCapabilities(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext, request *capabilityrequest.CapabilityRequest) ([]*hitl.PreparedApproval, error) {
	var reviews []*hitl.PreparedApproval
	collect := func(err error) error {
		var review *hitl.PreparedApproval
		if errors.As(err, &review) {
			reviews = append(reviews, review)
			return nil
		}
		return err
	}
	if err := collect(e.Boundary.preflightWriteRoot(ctx, tool, args, &tc)); err != nil {
		return nil, err
	}
	// Predicted roots belong only to review preparation.
	if request.WriteRoot != "" && len(reviews) > 0 {
		ctx = context.WithValue(ctx, capabilityReviewRootsKey{}, []string{request.WriteRoot})
	}
	if err := collect(e.Boundary.preflightReadPath(ctx, tool, args, &tc)); err != nil {
		return nil, err
	}
	if e.Boundary.sessionReadOverlay != nil {
		tc.Files.SessionReadPaths = append([]string(nil), e.Boundary.sessionReadOverlay(ctx, tc.Identity.SessionID, tc.Identity.ParentSessionID)...)
	}
	if request.ReadPath != "" {
		tc.Files.SessionReadPaths = append(tc.Files.SessionReadPaths, request.ReadPath)
	}
	sockets, err := e.Capabilities.preflightSocketCapability(ctx, tool, args, tc)
	if err := collect(err); err != nil {
		return nil, err
	}
	if sockets != nil {
		tc.Socket.SocketGrants = sockets.SocketGrants
		tc.Socket.AuthorizedSocketDigests = sockets.AuthorizedSocketDigests
	}
	// Socket reviews can also cover the requested direct-IP authority.
	if !reviewsCoverDirectIP(reviews) {
		_, err := e.Capabilities.preflightDirectIPCapability(ctx, tool, args, tc, false)
		if err := collect(err); err != nil {
			return nil, err
		}
	}
	tc.Direct.DirectIPRequested = request.DirectIP != nil
	if request.DirectIP != nil {
		tc.Direct.DirectIPDeclared = append([]string(nil), request.DirectIP.DeclaredDestinations...)
	}
	e.Boundary.applySessionListenGrant(ctx, &tc)
	e.Boundary.applySessionLoopbackGrant(ctx, &tc)
	if e.Capabilities.localNetworkGate == nil {
		_, err = e.Capabilities.preflightLocalListenCapability(ctx, tool, args, tc)
		if err := collect(err); err != nil {
			return nil, err
		}
		_, err = e.Capabilities.preflightLoopbackConnectCapability(ctx, tool, args, tc)
		if err := collect(err); err != nil {
			return nil, err
		}
		predictExecutionReviewCapabilities(&tc, request)
		if err := collect(e.Process.preflightExecutionCapability(ctx, tool, args, &tc)); err != nil {
			return nil, err
		}
		return reviews, nil
	}
	_, _, err = e.Capabilities.preflightLocalNetworkCapability(ctx, tool, args, tc)
	if err := collect(err); err != nil {
		return nil, err
	}
	predictExecutionReviewCapabilities(&tc, request)
	if err := collect(e.Process.preflightExecutionCapability(ctx, tool, args, &tc)); err != nil {
		return nil, err
	}
	return reviews, nil
}

func reviewsCoverDirectIP(reviews []*hitl.PreparedApproval) bool {
	for _, review := range reviews {
		for _, target := range review.Request.ApprovalPlan.Subject.Targets {
			if target.Kind == "direct_ip" {
				return true
			}
		}
	}
	return false
}

// These facts exist only while preparing a combined card, never as launch authority.
func predictExecutionReviewCapabilities(tc *tools.ToolContext, request *capabilityrequest.CapabilityRequest) {
	if request.LocalListen != nil {
		tc.Local.LocalListenGranted = true
		if len(request.LocalListen.Ports) > 0 {
			tc.Local.LocalListenPorts = request.LocalListen.Ports
		}
		if request.DirectIP != nil {
			tc.Local.LocalListenPorts = nil
		}
	}
	if request.LoopbackConnect != nil {
		tc.Local.LoopbackConnectGranted = true
		if len(request.LoopbackConnect.Ports) > 0 {
			tc.Local.LoopbackConnectPorts = request.LoopbackConnect.Ports
		}
		if request.DirectIP != nil {
			tc.Local.LoopbackConnectPorts = nil
		}
	}
	if len(request.SocketPaths) > 0 && len(tc.Socket.SocketGrants) == 0 {
		grants, reject := capabilityrequest.ResolveCapabilitySockets(request)
		if reject == nil {
			tc.Socket.SocketGrants = grants
		}
	}
}
