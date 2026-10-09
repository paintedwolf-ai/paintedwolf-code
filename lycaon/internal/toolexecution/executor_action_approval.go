package toolexecution

import (
	"github.com/lycaon/lycaon/internal/toolapproval"

	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/tools"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
)

func (e *Approvals) awaitApproval(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc tools.ToolContext,
	approvalResult *hitl.ApprovalResult,
	confReq confine.Request,
) (map[string]any, error) {
	action := hitl.ProposedAction{
		AgentPolicy:             policyWriteTargets(confReq, tc.ActiveRootPath()),
		Tool:                    tool,
		Args:                    args,
		Files:                   append(filesFromArgs(tool, args), policyWritePaths(confReq)...),
		ResolvedFiles:           append(ResolvedApprovalFiles(tool, args, tc), policyWritePaths(confReq)...),
		HostResources:           append([]string(nil), tc.HostResources...),
		HostResourceFamilies:    append([]string(nil), tc.HostResourceFamilies...),
		ProjectID:               tc.ProjectID,
		ProjectDir:              tc.ActiveRootPath(),
		SessionID:               tc.SessionID,
		RootSessionID:           tc.ChatSessionID(),
		SessionScratchRoot:      tc.SessionScratchDir,
		SocketGrants:            append([]confine.SocketGrant(nil), tc.SocketGrants...),
		SocketScopes:            append([]string(nil), tc.SocketScopes...),
		SocketGrantStates:       append([]string(nil), tc.SocketGrantStates...),
		AuthorizedSocketDigests: append([]string(nil), tc.AuthorizedSocketDigests...),
		AuthorizedDirectIP:      tc.DirectIPAuthorized,
		Contained:               hitl.ContainedForRequest(confReq),
		ActionID:                tc.ToolCallID,
		DirectIPRequested:       tc.DirectIPRequested,
		Visibility:              directIPVisibility(tc.DirectIPRequested),
		DeclaredDestinations:    append([]string(nil), tc.DirectIPDeclared...),
		PackageExecution:        tc.PackageExecution,
	}
	if meta, ok := e.Metadata.registry.Meta(tool); ok {
		action.ApprovalCategory = meta.ApprovalCategory
		action.ApprovalSubject = meta.ApprovalSubject
	}
	if tool == "command" || tool == "verify" || tool == "terminal_open" {
		action.Command = commandsurface.PrimaryCommandLine(args, nil)
	}
	if tool == "command_stop" && e.backgroundCommand != nil {
		handle, _ := args["handle"].(string)
		action.Command = e.backgroundCommand(tc.SessionID, handle)
	}
	return e.awaitActionApproval(ctx, action, args, tc, approvalResult)
}

func (e *Approvals) awaitActionApproval(ctx context.Context, action hitl.ProposedAction, args map[string]any, tc tools.ToolContext, approvalResult *hitl.ApprovalResult) (map[string]any, error) {
	tool := action.Tool
	approvalMatches := approvalRuleMatches(approvalResult)
	decision := approvalDecision(approvalResult)
	detection := detectionOf(approvalResult)
	var explanation *hitl.ApprovalExplanation
	var copy toolapproval.ApprovalExplanation
	if e.approvalExplainer != nil {
		copy = e.approvalExplainer.ExplainApproval(action)
	}
	// toolapproval.ExplainGate supplies per-gate copy checked by the coverage contract.
	if decision != nil {
		copy = toolapproval.ExplainGate(copy, decision)
	}
	if e.approvalExplainer != nil || decision != nil {
		explanation = &hitl.ApprovalExplanation{
			What:      copy.What,
			Who:       copy.Who,
			IfWrong:   copy.IfWrong,
			AllowLine: copy.AllowLine,
		}
	}
	title := fmt.Sprintf("Approve %s", tool)
	if len(approvalMatches) > 0 && approvalMatches[0].Command != "" {
		title = fmt.Sprintf("Approve command: %s", approvalMatches[0].Command)
	}
	grantDelta := ""
	if approvalResult != nil {
		grantDelta = approvalResult.GrantDelta
	}
	// Reserve a rationale placeholder only when a rationale will be requested.
	rationaleComing := e.aiRationale != nil && e.aiRationale.Enabled()
	review := toolApprovalRaise{
		Action:             action,
		Title:              title,
		ToolCallID:         tc.ToolCallID,
		ProjectID:          tc.ProjectID,
		ApprovalMatches:    approvalMatches,
		Explanation:        explanation,
		Decision:           decision,
		Detection:          detection,
		AIRationalePending: rationaleComing,
		AttachRationale:    rationaleComing,
		RationaleTC:        tc,
		RationaleTool:      tool,
		RationaleArgs:      args,
		GrantOffers:        e.grantOffers(action, approvalResult),
		GrantDelta:         grantDelta,
		Presence:           tc.Presence,
	}
	permission, err := e.Secrets.composeToolSecretReview(ctx, &review, tc)
	if err != nil {
		return nil, err
	}
	final, err := e.raiseAndWaitToolApproval(ctx, review)
	if err != nil {
		return nil, err
	}
	switch {
	case hitl.CheckpointAuthorizes(final):
		approveSecretPermission(tc.Secrets, permission)
		return args, nil
	case final.Status == hitl.DecisionStatusRejected || final.Status == hitl.DecisionStatusApproved:
		return nil, e.approvalRefusal(approvaloutcome.CodeApprovalDenied)
	case final.Status == hitl.DecisionStatusExpired:
		return nil, e.approvalRefusal(approvaloutcome.CodeApprovalExpired)
	case final.Status == hitl.DecisionStatusCanceled:
		return nil, e.approvalRefusal(approvaloutcome.CodeApprovalCanceled)
	default:
		return nil, fmt.Errorf("approval decision unresolved: %s", final.Status)
	}
}
