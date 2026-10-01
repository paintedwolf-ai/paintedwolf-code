package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func (e *DefaultToolExecutor) preflightWriteRoot(ctx context.Context, tool string, args map[string]any, tc *ToolContext) error {
	if !tc.Invocation.Contract.Supports(toolcontract.CapabilityWriteRoot) {
		return nil
	}
	request, reject := ParseCapabilityRequest(args)
	if reject != nil {
		return e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, reject)
	}
	if request == nil || request.WriteRoot == "" {
		return nil
	}
	if _, policy := confine.AgentPolicyPath(request.WriteRoot, ConfineRootsForAction(*tc)...); policy {
		if tool == "terminal_open" {
			return e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{Code: "POLICY_WRITE_REQUIRES_COMMAND", Data: map[string]any{"path": request.WriteRoot}})
		}
		return e.preparePolicyWrite(tc, request.WriteRoot)
	}
	if e.writeRootPreflight == nil {
		return e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"reason": "write-root approval is not configured"},
		})
	}
	authorized, denied, guidance, err := e.writeRootPreflight(ctx, tool, args, *tc, request.WriteRoot)
	if err != nil {
		return e.rejectApprovalErr(ctx, tool, tc.Agent, args, err)
	}
	if !authorized {
		data := map[string]any{"path": request.WriteRoot}
		if guidance != "" {
			data[UserGuidanceKey] = guidance
		}
		code := isolation.CodeWriteRootDenied
		if !denied {
			code = isolation.CodeApprovalUnavailable
			data["reason"] = "approval did not install authority"
		}
		return e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{
			Code: code, Data: data,
		})
	}
	return nil
}
