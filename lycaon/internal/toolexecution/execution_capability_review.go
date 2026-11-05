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
	for _, err := range e.reviewExecutionCapabilities(ctx, tool, args, tc) {
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *ProcessAuthority) reviewExecutionCapabilities(ctx context.Context, tool string, args map[string]any, tc *tools.ToolContext) []error {
	request, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		return []error{reject}
	}
	if request == nil || (!request.ProcessControl && !request.CredentialStore && !request.HostExecution) {
		return nil
	}
	tc.Execution.ProcessControl, tc.Execution.CredentialStore, tc.Execution.HostExecution = request.ProcessControl, request.CredentialStore, request.HostExecution
	fullAction := e.executionCapabilityAction(ctx, tool, args, *tc)
	if (request.ProcessControl || request.CredentialStore) && !fullAction.Execution.Contained.FSJailed {
		return []error{&toolrejection.ToolReject{Code: isolation.CodeExecutionBoundaryUnavailable}}
	}
	var errs []error
	for _, mode := range []string{"host_execution", "process_control", "credential_store"} {
		switch mode {
		case "host_execution":
			if !request.HostExecution {
				continue
			}
		case "process_control":
			if !request.ProcessControl {
				continue
			}
		case "credential_store":
			if !request.CredentialStore {
				continue
			}
		}
		subTC := *tc
		subTC.Execution.HostExecution = mode == "host_execution"
		subTC.Execution.ProcessControl = mode == "process_control"
		subTC.Execution.CredentialStore = mode == "credential_store"
		action := e.executionCapabilityAction(ctx, tool, args, subTC)
		result, err := e.Approvals.evaluatePreSpawn(ctx, action)
		if err != nil {
			return []error{err}
		}
		if result == nil {
			return []error{&toolrejection.ToolReject{Code: isolation.CodeApprovalUnavailable}}
		}
		if result.Denied {
			return []error{e.Approvals.rejectBoundaryPolicyDeny(ctx, tool, args, subTC, result)}
		}
		if result.Required() {
			if err := e.awaitExecutionCapability(ctx, action, subTC, result); err != nil {
				errs = append(errs, err)
				if !hitl.IsPreparedApproval(err) {
					return errs
				}
			}
		}
	}
	if len(errs) > 0 {
		return errs
	}
	tc.StampExecutionApproval(args, fullAction)
	return nil
}

func (e *ProcessAuthority) executionCapabilityAction(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext) hitl.ProposedAction {
	request := e.Boundary.actionConfineRequest(ctx, tc)
	action := proposedActionFromPolicy(e.Boundary.preInvokePolicyContext(tool, tc.ProfileID(), args, tc, request))
	action.Execution.ExecutionBoundaryDigest = tools.ExecutionBoundaryDigest(request)
	action.Presentation.Command = commandsurface.PrimaryCommandLine(args, nil)
	return action
}

func (e *ProcessAuthority) awaitExecutionCapability(ctx context.Context, action hitl.ProposedAction, tc tools.ToolContext, result *hitl.ApprovalResult) error {
	subject, title, impact, consequence := executionCapabilityCopy(tc.Execution.HostExecution, tc.Execution.CredentialStore && !tc.Execution.ProcessControl)
	targets := []hitl.ApprovalTarget{{Kind: string(subject), Label: action.Presentation.Command}}
	who := hitl.WhoAgentCommand
	if action.Execution.ProcessAccess != "" {
		subject = hitl.ApprovalSubjectAction
		who = hitl.WhoAgentAction
		title, impact, consequence = hitl.ProcessSignalTitle, hitl.ProcessSignalWhat, hitl.ProcessSignalIfWrong
		targets = action.Execution.ProcessTargets
		if action.Execution.ProcessAccess == "list" {
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
		Action: title, Tool: action.Invocation.Tool, Command: action.Presentation.Command,
		Impact: impact, Who: who, IfWrong: consequence,
		AllowLine: hitl.ExecutionAllowLine,
		Gate:      primary, Cited: cited, Detection: detectionOf(result), GrantDelta: result.GrantDelta,
	}, reasons, options, hitl.FaceContext{})
	if err != nil {
		return toolrejection.ApprovalPlanInvalid()
	}
	permission, err := e.Secrets.prepareSecretPermission(ctx, action.Invocation.Tool, action.Invocation.Args, tc)
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

func executionCapabilityCopy(host, credentialStore bool) (hitl.ApprovalSubjectKind, string, string, string) {
	if host {
		return hitl.ApprovalSubjectHostExecution, hitl.HostExecutionTitle, hitl.HostExecutionWhat, hitl.HostExecutionIfWrong
	}
	if credentialStore {
		return hitl.ApprovalSubjectCredentialStore, hitl.CredentialStoreTitle, hitl.CredentialStoreWhat, hitl.CredentialStoreIfWrong
	}
	return hitl.ApprovalSubjectProcessControl, hitl.ProcessControlTitle, hitl.ProcessControlWhat, hitl.ProcessControlIfWrong
}
