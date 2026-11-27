package toolexecution

import (
	"context"

	"github.com/lycaon/lycaon/internal/capabilityrequest"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
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
	return nil
}

func (e *Boundary) authorizeReadPath(ctx context.Context, tool string, args map[string]any, tc *tools.ToolContext, path string) error {
	if e.readPathPreflight == nil {
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args, &toolrejection.ToolReject{
			Code: isolation.CodeApprovalUnavailable,
			Data: map[string]any{"reason": "read-path approval is not configured"},
		})
	}
	res, err := e.readPathPreflight(ctx, tool, args, *tc, path)
	if err != nil {
		return e.Approvals.rejectApprovalErr(ctx, tool, tc.Identity.Agent, args, err)
	}
	if !res.Authorized {
		return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Identity.Agent, args,
			pathPreflightReject(tool, args, *tc, "read_path", path, isolation.CodeReadPathDenied, res))
	}
	return nil
}
