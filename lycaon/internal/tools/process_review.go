package tools

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/isolation"
)

// ProcessReviewer reviews host-resolved process identities before observation or mutation.
type ProcessReviewer func(context.Context, string, []hostprocess.Process) error

func (e *DefaultToolExecutor) processReviewer(tool string, args map[string]any, tc ToolContext) ProcessReviewer {
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
		result, err := e.evaluatePreSpawn(ctx, action)
		if err != nil {
			return err
		}
		if result == nil {
			return &ToolReject{Code: isolation.CodeApprovalUnavailable}
		}
		if result.Denied {
			return e.rejectBoundaryPolicyDeny(ctx, tool, args, tc, result)
		}
		if result.Required() {
			return e.awaitExecutionCapability(ctx, action, tc, result)
		}
		return ctx.Err()
	}
}
