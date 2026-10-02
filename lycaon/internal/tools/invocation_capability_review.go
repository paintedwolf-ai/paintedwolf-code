package tools

import (
	"context"
	"errors"
	"slices"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/pkg/api"
)

type capabilityReviewRootsKey struct{}
type capabilityReviewSecretKey struct{}

// reviewedSecretPermission is a combined card's secret release, held until
// the invocation boundary records it.
type reviewedSecretPermission struct {
	permission  *hitl.SecretPermission
	attestation string
}

// Secret targets remain available until every capability replays the held subject.
func approveCapabilitySecretPermission(ctx context.Context, tc ToolContext, permission *hitl.SecretPermission, attestation string) {
	if !hitl.HasPreparedApprovalAnswers(ctx) {
		approveSecretPermission(tc.Secrets, permission, attestation)
	}
}

// Reviews prepare together; execution preflight uses committed grants and answers.
func (e *DefaultToolExecutor) reviewInvocationCapabilities(ctx context.Context, tool string, args map[string]any, tc ToolContext) (context.Context, error) {
	request, reject := ParseCapabilityRequest(args)
	if reject != nil {
		return ctx, e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, reject)
	}
	if !multipleCapabilityFamilies(request) {
		return ctx, nil
	}
	permission, err := e.prepareSecretPermission(ctx, tool, args, tc)
	if err != nil {
		return ctx, err
	}
	reviews, err := e.prepareInvocationCapabilities(hitl.WithApprovalPreparation(ctx), tool, args, tc, request)
	if err != nil || len(reviews) == 0 {
		return ctx, err
	}
	action := hitl.ProposedAction{
		Tool: tool, Args: args, Command: commandsurface.PrimaryCommandLine(args, nil),
		ProjectID: tc.ProjectID, ProjectDir: tc.ActiveRootPath(),
		SessionID: tc.SessionID, RootSessionID: tc.ChatSessionID(), ActionID: tc.ToolCallID,
		Contained:            hitl.ContainedForRequest(e.actionConfineRequest(ctx, tc)),
		HostResources:        append([]string(nil), tc.HostResources...),
		HostResourceFamilies: append([]string(nil), tc.HostResourceFamilies...),
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
		return ctx, e.rejectApprovalErr(ctx, tool, tc.Agent, args, ApprovalPlanInvalid())
	}
	secretIncluded := slices.ContainsFunc(plan.Subject.Targets, func(target hitl.ApprovalTarget) bool { return target.Kind == "secret" })
	final, err := e.raiseAndWaitToolApproval(ctx, toolApprovalRaise{
		Action: action, Plan: plan, Decision: decision, Title: plan.Subject.Title,
		ToolCallID: tc.ToolCallID, ProjectID: tc.ProjectID,
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
		return ctx, e.rejectApprovalErr(ctx, tool, tc.Agent, args, isolationCheckpointReject(capabilityReviewDenialCode(plan), final))
	}
	if secretIncluded {
		ctx = context.WithValue(ctx, capabilityReviewSecretKey{}, reviewedSecretPermission{permission: permission, attestation: attestationOf(final)})
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

func multipleCapabilityFamilies(request *CapabilityRequest) bool {
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

func (e *DefaultToolExecutor) prepareInvocationCapabilities(ctx context.Context, tool string, args map[string]any, tc ToolContext, request *CapabilityRequest) ([]*hitl.PreparedApproval, error) {
	var reviews []*hitl.PreparedApproval
	collect := func(err error) error {
		var review *hitl.PreparedApproval
		if errors.As(err, &review) {
			reviews = append(reviews, review)
			return nil
		}
		return err
	}
	if err := collect(e.preflightWriteRoot(ctx, tool, args, &tc)); err != nil {
		return nil, err
	}
	// Predicted roots belong only to review preparation.
	if request.WriteRoot != "" && len(reviews) > 0 {
		ctx = context.WithValue(ctx, capabilityReviewRootsKey{}, []string{request.WriteRoot})
	}
	if err := collect(e.preflightReadPath(ctx, tool, args, &tc)); err != nil {
		return nil, err
	}
	if e.sessionReadOverlay != nil {
		tc.SessionReadPaths = append([]string(nil), e.sessionReadOverlay(ctx, tc.SessionID, tc.ParentSessionID)...)
	}
	if request.ReadPath != "" {
		tc.SessionReadPaths = append(tc.SessionReadPaths, request.ReadPath)
	}
	sockets, err := e.preflightSocketCapability(ctx, tool, args, tc)
	if err := collect(err); err != nil {
		return nil, err
	}
	if sockets != nil {
		tc.SocketGrants = sockets.SocketGrants
		tc.AuthorizedSocketDigests = sockets.AuthorizedSocketDigests
	}
	// Socket reviews can also cover the requested direct-IP authority.
	if !reviewsCoverDirectIP(reviews) {
		_, err := e.preflightDirectIPCapability(ctx, tool, args, tc, false)
		if err := collect(err); err != nil {
			return nil, err
		}
	}
	tc.DirectIPRequested = request.DirectIP != nil
	if request.DirectIP != nil {
		tc.DirectIPDeclared = append([]string(nil), request.DirectIP.DeclaredDestinations...)
	}
	e.applySessionListenGrant(ctx, &tc)
	e.applySessionLoopbackGrant(ctx, &tc)
	if e.localNetworkGate == nil {
		_, err = e.preflightLocalListenCapability(ctx, tool, args, tc)
		if err := collect(err); err != nil {
			return nil, err
		}
		_, err = e.preflightLoopbackConnectCapability(ctx, tool, args, tc)
		if err := collect(err); err != nil {
			return nil, err
		}
		predictExecutionReviewCapabilities(&tc, request)
		if err := collect(e.preflightExecutionCapability(ctx, tool, args, &tc)); err != nil {
			return nil, err
		}
		return reviews, nil
	}
	_, _, err = e.preflightLocalNetworkCapability(ctx, tool, args, tc)
	if err := collect(err); err != nil {
		return nil, err
	}
	predictExecutionReviewCapabilities(&tc, request)
	if err := collect(e.preflightExecutionCapability(ctx, tool, args, &tc)); err != nil {
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
func predictExecutionReviewCapabilities(tc *ToolContext, request *CapabilityRequest) {
	if request.LocalListen != nil {
		tc.LocalListenGranted = true
		if len(request.LocalListen.Ports) > 0 {
			tc.LocalListenPorts = request.LocalListen.Ports
		}
		if request.DirectIP != nil {
			tc.LocalListenPorts = nil
		}
	}
	if request.LoopbackConnect != nil {
		tc.LoopbackConnectGranted = true
		if len(request.LoopbackConnect.Ports) > 0 {
			tc.LoopbackConnectPorts = request.LoopbackConnect.Ports
		}
		if request.DirectIP != nil {
			tc.LoopbackConnectPorts = nil
		}
	}
	if len(request.SocketPaths) > 0 && len(tc.SocketGrants) == 0 {
		grants, reject := ResolveCapabilitySockets(request)
		if reject == nil {
			tc.SocketGrants = grants
		}
	}
}
