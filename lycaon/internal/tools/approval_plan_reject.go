package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/platform"
)

// ApprovalPlanInvalid reports a malformed host approval plan.
func ApprovalPlanInvalid() *ToolReject {
	return &ToolReject{Code: "HOST_APPROVAL_PLAN_INVALID"}
}

func (e *DefaultToolExecutor) rejectApprovalErr(
	ctx context.Context,
	tool, profileID string,
	args map[string]any,
	err error,
) error {
	if err == nil {
		return nil
	}
	var reject *ToolReject
	if errors.As(err, &reject) {
		return e.rejectBeforeInvoke(ctx, tool, profileID, args, reject)
	}
	return err
}

func isolationCheckpointReject(deniedCode string, final *hitl.CheckpointResponse) *ToolReject {
	if final == nil {
		return &ToolReject{Code: isolation.CodeApprovalUnavailable, Data: map[string]any{
			"reason": "capability approval returned no decision",
		}}
	}
	if final.Status == hitl.DecisionStatusRejected || final.Status == hitl.DecisionStatusApproved {
		reject := &ToolReject{Code: deniedCode}
		if final.Result != nil {
			AttachUserGuidance(reject, strings.TrimSpace(final.Result.Comments))
		}
		return reject
	}
	return &ToolReject{Code: isolation.CodeApprovalUnavailable, Data: map[string]any{
		"reason": "capability approval ended with status " + string(final.Status),
	}}
}

func (e *DefaultToolExecutor) rejectBoundaryPolicyDeny(
	ctx context.Context,
	tool string,
	args map[string]any,
	tc ToolContext,
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
	return e.rejectBeforeInvoke(ctx, tool, tc.Agent, args, policyBlockReject(decision, tool, tc.Agent))
}
