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
	if request == nil || len(request.ReadPaths) == 0 {
		return nil
	}
	for _, path := range request.ReadPaths {
		if err := e.authorizeReadPath(ctx, tool, args, tc, path); err != nil {
			return err
		}
	}
	if tc.Files.PackageExecution != nil {
		tc.Files.PackageExecution.ApprovedReadPaths = request.ReadPaths
	}
	if e.sessionReadOverlay != nil {
		tc.Files.SessionReadPaths = append([]string(nil), e.sessionReadOverlay(ctx, tc.Identity.SessionID, tc.Identity.ParentSessionID)...)
	}
	return nil
}

// ReviewRedirectRead reviews a shared-temp input redirect or sourced script before opening it.
func (e *Boundary) ReviewRedirectRead(ctx context.Context, tc tools.ToolContext, operator, path string) (tools.ToolContext, error) {
	if e.sessionReadOverlay != nil {
		tc.Files.SessionReadPaths = append([]string(nil), e.sessionReadOverlay(ctx, tc.Identity.SessionID, tc.Identity.ParentSessionID)...)
	}
	args := map[string]any{"command": operator + " " + path}
	if err := e.authorizeReadPath(ctx, "command", args, &tc, path); err != nil {
		return tc, err
	}
	if e.sessionReadOverlay != nil {
		tc.Files.SessionReadPaths = append([]string(nil), e.sessionReadOverlay(ctx, tc.Identity.SessionID, tc.Identity.ParentSessionID)...)
	}
	return tc, nil
}

func (e *Boundary) authorizeReadPath(ctx context.Context, tool string, args map[string]any, tc *tools.ToolContext, path string) error {
	if e.readPathPreflight == nil {
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"reason": "read-path approval is not configured"},
		})
	}
	authorized, denied, guidance, err := e.readPathPreflight(ctx, tool, args, *tc, path)
	if err != nil {
		return e.Approvals.rejectApprovalErr(ctx, tool, tc.Identity.Agent, args, err)
	}
	if !authorized {
		data := map[string]any{"path": path}
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
	return nil
}
