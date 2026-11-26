package toolexecution

import (
	"context"
	"slices"

	"github.com/lycaon/lycaon/internal/capabilityrequest"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
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

// ReviewRedirectRead asks for an input redirect or sourced script the way a
// native read of the same file asks, then for protected-read access when the
// file needs it. A refusal returns tc unchanged.
func (e *Boundary) ReviewRedirectRead(ctx context.Context, tc tools.ToolContext, operator, path string) (tools.ToolContext, error) {
	args := map[string]any{"command": operator + " " + path}
	access, err := e.reviewOutsideRead(ctx, tc, args, path)
	if err != nil {
		return tc, err
	}
	reviewed := tc
	reviewed.Files.ApprovedFileAccess = append(slices.Clone(tc.Files.ApprovedFileAccess), access...)
	if err := e.authorizeReadPath(ctx, "command", args, &reviewed, path); err != nil {
		return tc, err
	}
	if e.sessionReadOverlay != nil {
		reviewed.Files.SessionReadPaths = append([]string(nil), e.sessionReadOverlay(ctx, tc.Identity.SessionID, tc.Identity.ParentSessionID)...)
	}
	// A single-use approval installs no session grant but covers this open.
	reviewed.Files.SessionReadPaths = append(slices.Clone(reviewed.Files.SessionReadPaths), path)
	return reviewed, nil
}

// reviewOutsideRead evaluates one outside file read through the action gate
// and returns the exact access an approval installs for this invocation.
func (e *Boundary) reviewOutsideRead(ctx context.Context, tc tools.ToolContext, args map[string]any, path string) ([]hitl.GrantedPathDelta, error) {
	if e.Approvals.approvalGate == nil {
		return nil, e.Rejections.rejectBeforeInvoke(ctx, "command", tc.Identity.Agent, args, &toolrejection.ToolReject{
			Code: isolation.CodeApprovalUnavailable, Data: map[string]any{"reason": "file approval is not configured"},
		})
	}
	confReq := e.actionConfineRequest(ctx, tc)
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			NativeInputs:       tc.Invocation.NativeInputs,
			BoundStages:        toolcontract.CloneExecutionStages(tc.Invocation.NativeStages),
			Tool:               "command",
			EntryTool:          tc.Invocation.EntryTool,
			Args:               args,
			Files:              []string{path},
			ResolvedFiles:      []string{fspath.CanonicalPath(path)},
			NativePathTargets:  []hitl.NativePathTarget{{Path: path}},
			ApprovedFileAccess: append([]hitl.GrantedPathDelta(nil), tc.Files.ApprovedFileAccess...),
			ActionID:           tc.EffectActionID(),
		},
		Scope: hitl.ActionScope{
			ProjectID:          tc.Identity.ProjectID,
			ProjectDir:         tc.ActiveRootPath(),
			SessionID:          tc.Identity.SessionID,
			RootSessionID:      tc.ChatSessionID(),
			SessionScratchRoot: tc.Host.SessionScratchDir,
			SessionOutputRoot:  tc.Host.SessionOutputDir,
			GrantedWriteRoots:  append([]string(nil), confReq.GrantedWriteRoots...),
		},
		Execution: hitl.ActionExecution{Contained: hitl.ContainedForRequest(confReq)},
	}
	action.Presentation.Command = args["command"].(string)
	result, err := e.Approvals.approvalGate.Evaluate(ctx, action)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	if result.Denied {
		return nil, &toolrejection.ToolReject{Code: result.DenyCode, Data: map[string]any{"path": result.DenySubject, "tool": "command"}}
	}
	if result.Required() {
		if _, err := e.Approvals.awaitActionApproval(ctx, action, args, tc, result); err != nil {
			return nil, err
		}
	}
	return result.FileAccess, nil
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
