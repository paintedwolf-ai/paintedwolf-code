package tools

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
)

func (e *DefaultToolExecutor) awaitSocketCapabilities(
	ctx context.Context,
	action hitl.ProposedAction,
	actionDigest string,
	grants []confine.SocketGrant,
	chatByPair map[string]struct{},
	tc ToolContext,
	approval *hitl.ApprovalResult,
	direct *directIPApprovalReview,
) error {
	if len(grants) == 0 {
		return nil
	}
	title := "Allow local services"
	if len(grants) == 1 {
		title = "Allow local service: " + filepath.Base(grants[0].ApprovedPath)
	}
	explanation := &hitl.ApprovalExplanation{
		What:      fmt.Sprintf(hitl.SocketWhatFormat, len(grants)),
		Who:       hitl.WhoAgentCommand,
		IfWrong:   hitl.SocketIfWrong,
		AllowLine: hitl.SocketAllowLine,
	}
	if tc.Invocation.Contract.SocketArg != "" {
		explanation.Who, explanation.IfWrong = hitl.WhoAgentAction, hitl.SocketDialIfWrong
	}
	targets := make([]hitl.SocketCapabilityTarget, 0, len(grants))
	approvalTargets := make([]hitl.ApprovalTarget, 0, len(grants))
	authorityTargets := make([]hitl.ApprovalSocketTarget, 0, len(grants))
	for _, grant := range grants {
		_, alreadyChat := chatByPair[socketGrantPairKey(grant)]
		targets = append(targets, hitl.SocketCapabilityTarget{ApprovedPath: grant.ApprovedPath, ResolvedPath: grant.ResolvedPath, AlreadyChatGranted: alreadyChat})
		approvalTargets = append(approvalTargets, hitl.ApprovalTarget{Kind: "socket", Label: grant.ApprovedPath, Details: map[string]any{"approved_path": grant.ApprovedPath, "resolved_path": grant.ResolvedPath}})
		authorityTargets = append(authorityTargets, hitl.ApprovalSocketTarget{ApprovedPath: grant.ApprovedPath, ResolvedPath: grant.ResolvedPath})
	}
	socketPayload := &hitl.SocketCapability{
		Targets:            targets,
		EffectiveAuthority: hitl.SocketAuthorityOutsideSandboxDaemon,
	}
	axisOffers := []hitl.ApprovalGrantOffer(nil)
	if approval != nil && approval.Decision != nil && approval.Decision.Reuse().Offered() {
		axisOffers = SocketExecutionGrantOffers(action, grants)
	}
	absorbed := e.absorbedGrantOffers(action, approval)
	grantOffers := append(append([]hitl.ApprovalGrantOffer(nil), axisOffers...), absorbed...)
	permit := socketPermitDelta(action, actionDigest, tc, authorityTargets)
	options := socketCapabilityOptions(permit, axisOffers, absorbed)
	options = attachRealizationWriteRoots(options, action, tc.RealizationWriteRoots)
	planAction := action
	subjectKind := hitl.ApprovalSubjectSocketSet
	presentationAction := "Connect to local services"
	coalesceKey := socketSetCoalesceKey(action, grants)
	if direct != nil {
		planAction = direct.Action
		title = "Allow command capabilities"
		subjectKind = hitl.ApprovalSubjectActionSet
		presentationAction = "Use local services and direct network access"
		approvalTargets = append(approvalTargets, hitl.ApprovalTarget{
			Kind: "direct_ip", Label: DirectIPTargetLabel(direct.Action.Command),
			Details: map[string]any{"declared_destinations": direct.Lease.DeclaredDestinations, "visibility": hitl.DirectIPVisibilityUnobserved},
		})
		for i := range options {
			options[i].Authority = append(options[i].Authority, combinedDirectIPAuthority(options[i], *direct, tc)...)
			options[i].Coverage += "; direct network access for the same action"
		}
		coalesceKey += ":direct_ip:" + direct.Lease.ActionDigest
		explanation.What = fmt.Sprintf("Connect to %d local service target(s) and use direct network access whose destinations the app cannot observe.", len(grants))
		explanation.IfWrong = "The local services and unobserved network destinations can cause effects outside the filesystem sandbox."
		explanation.AllowLine = "the listed local services and direct network access for this action"
	}
	primaryGate, cited, reasons := hitl.PresentDecision(approvalDecision(approval))
	options = append(options, e.quietOptionsFor(planAction, approvalDecision(approval))...)
	plan, err := hitl.NewApprovalPlan(planAction, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: subjectKind, Title: title, Targets: approvalTargets,
	}, hitl.ApprovalPresentation{
		Action: presentationAction, Tool: planAction.Tool, Command: planAction.Command,
		Impact: explanation.What, Who: explanation.Who, IfWrong: explanation.IfWrong, AllowLine: explanation.AllowLine,
		Gate: primaryGate, Cited: cited, GrantDelta: approvalGrantDelta(approval),
		Detection: detectionOf(approval),
	}, reasons, options, hitl.FaceContext{})
	if err != nil {
		return ApprovalPlanInvalid()
	}
	permission, err := e.prepareSecretPermission(ctx, action.Tool, action.Args, tc)
	if err != nil {
		return err
	}
	plan, err = hitl.ComposeSecretPermission(plan, planAction, permission, hitl.FaceContext{})
	if err != nil {
		return ApprovalPlanInvalid()
	}
	coalesceKey = permission.Key(coalesceKey)
	final, err := e.raiseAndWaitToolApproval(ctx, toolApprovalRaise{
		Action:                 planAction,
		Plan:                   plan,
		SecretScreenHit:        permission != nil,
		Title:                  title,
		ToolCallID:             tc.ToolCallID,
		ProjectID:              tc.ProjectID,
		Explanation:            explanation,
		GrantOffers:            grantOffers,
		CoalesceKey:            coalesceKey,
		SocketCapability:       socketPayload,
		SkipGrantOfferAutofill: true,
		ApprovalMatches:        approvalRuleMatches(approval),
		Decision:               approvalDecision(approval),
		Detection:              detectionOf(approval),
		GrantDelta:             approvalGrantDelta(approval),
	})
	if err != nil {
		return err
	}
	if !hitl.CheckpointAuthorizes(final) {
		return isolationCheckpointReject(isolation.CodeSocketPathDenied, final)
	}
	approveCapabilitySecretPermission(ctx, tc, permission)
	return nil
}
