package toolexecution

import (
	"context"

	"github.com/lycaon/lycaon/internal/capabilityrequest"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/settings"
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

func approvalRuleDeniedReject(tool, profileID, path, cmd string, rule *settings.ApprovalRule) *toolrejection.ToolReject {
	data := map[string]any{"tool": tool, "profile": profileID}
	if path != "" {
		data["path"] = path
	}
	if cmd != "" {
		data["command"] = cmd
	}
	if rule != nil {
		data["rule_unit_id"] = rule.Source.UnitID
		data["rule_pack_id"] = rule.Source.PackID
		data["rule_scope"] = string(rule.Source.Scope)
		data["rule_category"] = string(rule.Category)
		data["rule_pattern"] = rule.Pattern
		data["rule_match_count"] = "1"
	}
	return &toolrejection.ToolReject{
		Code:         "APPROVAL_RULE_DENIED",
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
	if res.DenialCause == DenialCauseApprovalRule || res.DeniedRule != nil {
		return approvalRuleDeniedReject(tool, tc.Identity.Agent, path, commandsurface.PrimaryCommandLine(args, nil), res.DeniedRule)
	}
	switch res.DenialCause {
	case DenialCauseControlPlanePolicy:
		return &toolrejection.ToolReject{
			Code: isolation.CodeControlPlaneDenied,
			Data: map[string]any{"path": path, "capability": capability},
		}
	case DenialCauseUserDeclined, DenialCausePriorUserDeclined:
		data := map[string]any{"path": path}
		if res.UserGuidance != "" {
			data[toolrejection.UserGuidanceKey] = res.UserGuidance
		}
		return &toolrejection.ToolReject{Code: deniedCode, Data: data}
	default:
		return &toolrejection.ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"path": path, "reason": "approval did not install authority"},
		}
	}
}
