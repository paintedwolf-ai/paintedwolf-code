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
			Tool: tool, Args: args, ProjectID: tc.Identity.ProjectID, ProjectDir: tc.ActiveRootPath(),
			SessionID: tc.Identity.SessionID, RootSessionID: tc.ChatSessionID(), ActionID: tc.Identity.ToolCallID,
			SessionScratchRoot: tc.Host.SessionScratchDir,
			Contained:          hitl.ContainedForRequest(e.actionConfineRequest(ctx, tc)),
		}
		files, policies := map[string]bool{}, map[string]bool{}
		for _, change := range changes {
			for _, path := range []string{change.Path, change.FromPath} {
				if path != "" && !files[path] {
					files[path] = true
					action.Files = append(action.Files, path)
					action.ResolvedFiles = append(action.ResolvedFiles, fspath.CanonicalPath(path))
				}
				if change.Preview.Target == "index" || policies[path] {
					continue
				}
				if target, policy := tc.AgentPolicyTarget(path); policy {
					policies[path] = true
					action.AgentPolicy = append(action.AgentPolicy, target)
				}
			}
			action.FileChanges = append(action.FileChanges, change.Preview)
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
