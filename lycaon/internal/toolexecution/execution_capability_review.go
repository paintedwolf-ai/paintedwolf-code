package toolexecution

import (
	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
)

func (e *ProcessAuthority) preflightExecutionCapability(ctx context.Context, tool string, args map[string]any, tc *tools.ToolContext) error {
	request, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		return reject
	}
	if request == nil || (!request.ProcessControl && !request.HostExecution) {
		return nil
	}
	tc.Execution.ProcessControl, tc.Execution.HostExecution = request.ProcessControl, request.HostExecution
	action := e.executionCapabilityAction(ctx, tool, args, *tc)
	if request.ProcessControl && !action.Contained.FSJailed {
		return &toolrejection.ToolReject{Code: isolation.CodeExecutionBoundaryUnavailable}
	}
	result, err := e.Approvals.evaluatePreSpawn(ctx, action)
	if err != nil {
		return err
	}
	if result == nil {
		return &toolrejection.ToolReject{Code: isolation.CodeApprovalUnavailable}
	}
	if result.Denied {
		return e.Approvals.rejectBoundaryPolicyDeny(ctx, tool, args, *tc, result)
	}
	if result.Required() {
		if err := e.awaitExecutionCapability(ctx, action, *tc, result); err != nil {
			return err
		}
	}
	tc.StampExecutionApproval(args, action)
	return nil
}

func (e *ProcessAuthority) executionCapabilityAction(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext) hitl.ProposedAction {
	request := e.Boundary.actionConfineRequest(ctx, tc)
	action := proposedActionFromPolicy(e.Boundary.preInvokePolicyContext(tool, tc.ProfileID(), args, tc, request))
	action.ExecutionBoundaryDigest = tools.ExecutionBoundaryDigest(request)
	action.Command = commandsurface.PrimaryCommandLine(args, nil)
	return action
}

func (e *ProcessAuthority) awaitExecutionCapability(ctx context.Context, action hitl.ProposedAction, tc tools.ToolContext, result *hitl.ApprovalResult) error {
	subject, title, impact, consequence := executionCapabilityCopy(tc.Execution.HostExecution)
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
	offers := e.Approvals.grantOffers(action, result)
	for _, offer := range offers {
		options = append(options, hitl.GrantOption(offer))
	}
	options = append(options, e.Approvals.quietOptionsFor(action, result.Decision)...)
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
		return toolrejection.ApprovalPlanInvalid()
	}
	permission, err := e.Secrets.prepareSecretPermission(ctx, action.Tool, action.Args, tc)
	if err != nil {
		return err
	}
	plan, err = hitl.ComposeSecretPermission(plan, action, permission, hitl.FaceContext{})
	if err != nil {
		return toolrejection.ApprovalPlanInvalid()
	}
	final, err := e.Approvals.raiseAndWaitToolApproval(ctx, toolApprovalRaise{
		Action: action, Plan: plan, Title: title, ToolCallID: tc.Identity.ToolCallID, ProjectID: tc.Identity.ProjectID,
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
	approveCapabilitySecretPermission(ctx, tc, permission)
	return nil
}

func executionCapabilityCopy(host bool) (hitl.ApprovalSubjectKind, string, string, string) {
	if host {
		return hitl.ApprovalSubjectHostExecution, hitl.HostExecutionTitle, hitl.HostExecutionWhat, hitl.HostExecutionIfWrong
	}
	return hitl.ApprovalSubjectProcessControl, hitl.ProcessControlTitle, hitl.ProcessControlWhat, hitl.ProcessControlIfWrong
}
