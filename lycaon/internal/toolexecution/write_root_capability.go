package toolexecution

import (
	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func (e *Boundary) preflightWriteRoot(ctx context.Context, tool string, args map[string]any, tc *tools.ToolContext) error {
	if !tc.Invocation.Contract.Supports(toolcontract.CapabilityWriteRoot) {
		return nil
	}
	request, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Agent, args, reject)
	}
	if request == nil || request.WriteRoot == "" {
		return nil
	}
	if _, policy := confine.AgentPolicyPath(request.WriteRoot, tools.ConfineRootsForAction(*tc)...); policy {
		if tool == "terminal_open" {
			return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &toolrejection.ToolReject{Code: "POLICY_WRITE_REQUIRES_COMMAND", Data: map[string]any{"path": request.WriteRoot}})
		}
		return e.preparePolicyWrite(tc, request.WriteRoot)
	}
	if e.writeRootPreflight == nil {
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &toolrejection.ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"reason": "write-root approval is not configured"},
		})
	}
	authorized, denied, guidance, err := e.writeRootPreflight(ctx, tool, args, *tc, request.WriteRoot)
	if err != nil {
		return e.Approvals.rejectApprovalErr(ctx, tool, tc.Agent, args, err)
	}
	if !authorized {
		data := map[string]any{"path": request.WriteRoot}
		if guidance != "" {
			data[toolrejection.UserGuidanceKey] = guidance
		}
		code := isolation.CodeWriteRootDenied
		if !denied {
			code = isolation.CodeApprovalUnavailable
			data["reason"] = "approval did not install authority"
		}
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &toolrejection.ToolReject{
			Code: code, Data: data,
		})
	}
	return nil
}
