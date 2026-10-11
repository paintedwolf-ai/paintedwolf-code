package toolexecution

import (
	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"

	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func (e *Boundary) preflightReadPath(ctx context.Context, tool string, args map[string]any, tc *tools.ToolContext) error {
	if !tc.Invocation.Contract.Supports(toolcontract.CapabilityReadPath) {
		return nil
	}
	request, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, reject)
	}
	if request == nil || request.ReadPath == "" {
		return nil
	}
	if e.readPathPreflight == nil {
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"reason": "read-path approval is not configured"},
		})
	}
	authorized, denied, guidance, err := e.readPathPreflight(ctx, tool, args, *tc, request.ReadPath)
	if err != nil {
		return e.Approvals.rejectApprovalErr(ctx, tool, tc.Identity.Agent, args, err)
	}
	if !authorized {
		data := map[string]any{"path": request.ReadPath}
		if guidance != "" {
			data[toolrejection.UserGuidanceKey] = guidance
		}
		code := isolation.CodeReadPathDenied
		if !denied {
			code = isolation.CodeApprovalUnavailable
			data["reason"] = "approval did not install authority"
		}
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{
			Code: code, Data: data,
		})
	}
	if tc.Files.PackageExecution != nil {
		tc.Files.PackageExecution.ApprovedReadPaths = []string{request.ReadPath}
	}
	return nil
}
