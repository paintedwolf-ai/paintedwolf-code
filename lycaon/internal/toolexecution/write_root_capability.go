package toolexecution

import (
	"context"

	"github.com/lycaon/lycaon/internal/capabilityrequest"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func (e *Boundary) preflightWriteRoot(ctx context.Context, tool string, args map[string]any, tc *tools.ToolContext) error {
	if !tc.Invocation.Contract.Supports(toolcontract.CapabilityWriteRoot) {
		return nil
	}
	request, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, reject)
	}
	if request == nil || request.WriteRoot == "" {
		return nil
	}
	if _, policy := confine.AgentPolicyPath(request.WriteRoot, tools.ConfineRootsForAction(*tc)...); policy {
		if tc.Invocation.Contract.Owner == "terminals" {
			return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{Code: "POLICY_WRITE_REQUIRES_COMMAND", Data: map[string]any{"path": request.WriteRoot}})
		}
		return e.preparePolicyWrite(tc, request.WriteRoot)
	}
	return e.reviewWriteRoot(ctx, tool, args, *tc, request.WriteRoot)
}

// ReviewRedirectRoot reviews the destination before its producer starts.
func (e *Boundary) ReviewRedirectRoot(ctx context.Context, tc tools.ToolContext, operator, path, root string) (tools.ToolContext, error) {
	tc.Files.GrantedWriteRoots = e.actionConfineRequest(ctx, tc).GrantedWriteRoots
	if confine.PathWithinRoots(path, tc.Files.GrantedWriteRoots) {
		return tc, nil
	}
	args := map[string]any{"command": operator + " " + path}
	if err := e.reviewWriteRoot(ctx, "command", args, tc, root); err != nil {
		return tc, err
	}
	tc.Files.GrantedWriteRoots = e.actionConfineRequest(ctx, tc).GrantedWriteRoots
	return tc, nil
}

func (e *Boundary) reviewWriteRoot(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext, root string) error {
	if e.writeRootPreflight == nil {
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"reason": "write-root approval is not configured"},
		})
	}
	res, err := e.writeRootPreflight(ctx, tool, args, tc, root)
	if err != nil {
		return e.Approvals.rejectApprovalErr(ctx, tool, tc.Identity.Agent, args, err)
	}
	if !res.Authorized {
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args,
			pathPreflightReject(tool, args, tc, "write_root", root, isolation.CodeWriteRootDenied, res))
	}
	return nil
}

func approvalRuleDeniedReject(tool, profileID, path, cmd string, rules []hitl.ApprovalRuleMatch) *toolrejection.ToolReject {
	data := denyRejectData(platform.PolicyContext{ToolName: tool, ProfileID: profileID}, &hitl.ApprovalResult{DenySubject: path, MatchedRules: rules})
	if _, ruleCommand := data["command"]; !ruleCommand && cmd != "" {
		data["command"] = cmd
	}
	return &toolrejection.ToolReject{
		Code:         hitl.CodeApprovalRuleDenied,
		Data:         data,
		FailureClass: api.FailureClassPolicyRejection,
	}
}

func pathPreflightReject(
	tool string,
	args map[string]any,
	tc tools.ToolContext,
	capability, path, deniedCode string,
	res PathPreflightResult,
) *toolrejection.ToolReject {
	switch res.DenialCause {
	case isolation.PathDenialApprovalRule:
		return approvalRuleDeniedReject(tool, tc.Identity.Agent, path, commandsurface.PrimaryCommandLine(args, nil), res.DeniedRules)
	case isolation.PathDenialControlPlanePolicy:
		return &toolrejection.ToolReject{
			Code: isolation.CodeControlPlaneDenied,
			Data: map[string]any{"path": path, "capability": capability},
		}
	case isolation.PathDenialUserDeclined, isolation.PathDenialPriorUserDeclined, isolation.PathDenialNonHumanRejected:
		reject := approvalRefusalReject(deniedCode, res.DenialCause, res.ApprovalResolvedBy, res.UserGuidance)
		reject.Data["path"] = path
		return reject
	default:
		return &toolrejection.ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"path": path, "reason": "approval did not install authority"},
		}
	}
}
