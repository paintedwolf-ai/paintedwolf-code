package toolexecution

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
)

func (e *Boundary) fileChangeReviewer(tool string, args map[string]any, tc tools.ToolContext) tools.FileChangeReviewer {
	return func(ctx context.Context, changes []tools.FileChange) error {
		if e.Approvals.approvalGate == nil {
			unwired := tc
			unwired.Files.FileChangeReview = nil
			return unwired.ReviewFileChanges(ctx, changes...)
		}
		action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: tool,
Args: args,
ActionID: tc.Identity.ToolCallID,
},
Scope: hitl.ActionScope{
ProjectID: tc.Identity.ProjectID,
ProjectDir: tc.ActiveRootPath(),
SessionID: tc.Identity.SessionID,
RootSessionID: tc.ChatSessionID(),
SessionScratchRoot: tc.Host.SessionScratchDir,
},
Execution: hitl.ActionExecution{
Contained: hitl.ContainedForRequest(e.actionConfineRequest(ctx, tc)),
},
}
		files, policies := map[string]bool{}, map[string]bool{}
		for _, change := range changes {
			for _, path := range []string{change.Path, change.FromPath} {
				if path != "" && !files[path] {
					files[path] = true
					action.Invocation.Files = append(action.Invocation.Files, path)
					action.Invocation.ResolvedFiles = append(action.Invocation.ResolvedFiles, fspath.CanonicalPath(path))
				}
				if change.Preview.Target == "index" || policies[path] {
					continue
				}
				if target, policy := tc.AgentPolicyTarget(path); policy {
					policies[path] = true
					action.Mutations.AgentPolicy = append(action.Mutations.AgentPolicy, target)
				}
			}
			action.Mutations.FileChanges = append(action.Mutations.FileChanges, change.Preview)
		}
		result, err := e.Approvals.approvalGate.Evaluate(ctx, action)
		if err != nil {
			return err
		}
		if result != nil && result.Denied {
			return &toolrejection.ToolReject{Code: result.DenyCode, Data: map[string]any{"path": result.DenySubject, "tool": tool}}
		}
		if !result.Required() {
			return nil
		}
		if tc.ConsumeContentApproval(changes) {
			return nil
		}
		if e.Approvals.checkpointMgr == nil {
			return fmt.Errorf("file change approval checkpoints not configured")
		}
		_, err = e.Approvals.awaitActionApproval(ctx, action, args, tc, result)
		return err
	}
}
