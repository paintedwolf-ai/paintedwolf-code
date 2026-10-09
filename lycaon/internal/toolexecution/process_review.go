package toolexecution

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/isolation"
)

// ProcessReviewer reviews host-resolved process identities before observation or mutation.

func (e *Capabilities) processReviewer(tool string, args map[string]any, tc tools.ToolContext) tools.ProcessReviewer {
	return func(ctx context.Context, operation string, processes []hostprocess.Process) error {
		action := e.executionCapabilityAction(ctx, tool, args, tc)
		action.ProcessAccess = operation
		action.ExecutionBoundaryDigest = ""
		if operation == "list" {
			action.ProcessTargets = []hitl.ApprovalTarget{{Kind: "process_inventory", Label: hitl.ProcessListTitle, Details: map[string]any{"args": args}}}
		} else {
			for _, process := range processes {
				action.ProcessTargets = append(action.ProcessTargets, hitl.ApprovalTarget{Kind: "process", Label: fmt.Sprintf("Send %s to %s (PID %d)", args["signal"], process.Name, process.PID), Details: map[string]any{"args": map[string]any{"pid": process.PID, "instance": process.Instance, "uid": process.UID, "executable": process.Executable, "signal": args["signal"]}}})
			}
		}
		result, err := e.Approvals.evaluatePreSpawn(ctx, action)
		if err != nil {
			return err
		}
		if result == nil {
			return &toolrejection.ToolReject{Code: isolation.CodeApprovalUnavailable}
		}
		if result.Denied {
			return e.Approvals.rejectBoundaryPolicyDeny(ctx, tool, args, tc, result)
		}
		if result.Required() {
			return e.awaitExecutionCapability(ctx, action, tc, result)
		}
		return ctx.Err()
	}
}
