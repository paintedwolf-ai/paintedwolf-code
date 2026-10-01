package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func (e *DefaultToolExecutor) preflightReadPath(ctx context.Context, tool string, args map[string]any, tc *ToolContext) error {
	if !tc.Invocation.Contract.Supports(toolcontract.CapabilityReadPath) {
		return nil
	}
	request, reject := ParseCapabilityRequest(args)
	if reject != nil {
		return e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, reject)
	}
	if request == nil || request.ReadPath == "" {
		return nil
	}
	if e.readPathPreflight == nil {
		return e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"reason": "read-path approval is not configured"},
		})
	}
	authorized, denied, guidance, err := e.readPathPreflight(ctx, tool, args, *tc, request.ReadPath)
	if err != nil {
		return e.rejectApprovalErr(ctx, tool, tc.Agent, args, err)
	}
	if !authorized {
		data := map[string]any{"path": request.ReadPath}
		if guidance != "" {
			data[UserGuidanceKey] = guidance
		}
		code := isolation.CodeReadPathDenied
		if !denied {
			code = isolation.CodeApprovalUnavailable
			data["reason"] = "approval did not install authority"
		}
		return e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, &ToolReject{
			Code: code, Data: data,
		})
	}
	if tc.PackageExecution != nil {
		tc.PackageExecution.ApprovedReadPaths = []string{request.ReadPath}
	}
	return nil
}
