package toolexecution

import (
	"github.com/lycaon/lycaon/internal/toolprofiles"

	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"strings"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/platform"
)

// ApprovalPlanInvalid reports a malformed host approval plan.

func (e *Approvals) rejectApprovalErr(
	ctx context.Context,
	tool, profileID string,
	args map[string]any,
	err error,
) error {
	if err == nil {
		return nil
	}
	var reject *toolrejection.ToolReject
	if errors.As(err, &reject) {
		return e.Rejections.rejectBeforeInvoke(ctx, tool, profileID, args, reject)
	}
	return err
}

func isolationCheckpointReject(deniedCode string, final *hitl.CheckpointResponse) *toolrejection.ToolReject {
	if final == nil {
		return &toolrejection.ToolReject{Code: isolation.CodeApprovalUnavailable, Data: map[string]any{
			"reason": "capability approval returned no decision",
		}}
	}
	if final.Status == hitl.DecisionStatusRejected || final.Status == hitl.DecisionStatusApproved {
		reject := &toolrejection.ToolReject{Code: deniedCode}
		if final.Result != nil {
			toolrejection.AttachUserGuidance(reject, strings.TrimSpace(final.Result.Comments))
		}
		return reject
	}
	return &toolrejection.ToolReject{Code: isolation.CodeApprovalUnavailable, Data: map[string]any{
		"reason": "capability approval ended with status " + string(final.Status),
	}}
}

func (e *Approvals) rejectBoundaryPolicyDeny(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc tools.ToolContext,
	approval *hitl.ApprovalResult,
) error {
	eval := platform.PolicyContext{
		ToolName: tool, ProfileID: tc.Agent, ToolArgs: args,
		ProjectID: tc.ProjectID, ProjectDir: tc.ActiveRootPath(),
		SessionID: tc.SessionID, ParentSessionID: tc.ParentSessionID,
	}
	decision := &platform.PolicyDecision{
		Blocked: true, RejectCode: approval.DenyCode,
		RejectData: denyRejectData(eval, approval), BlockReason: approval.DenyCode,
		Approval: approval,
	}
	e.recordToolDenied(ctx, eval, decision, approval)
	return e.Rejections.rejectBeforeInvoke(ctx, tool, tc.Agent, args, toolprofiles.PolicyBlockReject(decision, tool, tc.Agent))
}
