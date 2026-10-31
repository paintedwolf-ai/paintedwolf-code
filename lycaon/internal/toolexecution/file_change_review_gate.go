package toolexecution

import (
	"context"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func (e *Boundary) fileChangeReviewer(tool string, args map[string]any, tc tools.ToolContext) tools.FileChangeReviewer {
	var classifyOnce sync.Once
	var classifyPolicy func(string) (hitl.AgentPolicyTarget, bool)
	policyTarget := func(path string) (hitl.AgentPolicyTarget, bool) {
		classifyOnce.Do(func() {
			classifyPolicy = hitl.AgentPolicyTargetClassifier(tools.HostWriteRoot(tc), tools.ConfineRootsForAction(tc)...)
		})
		return classifyPolicy(path)
	}
	return func(ctx context.Context, changes []tools.FileChange) error {
		if e.Approvals.approvalGate == nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			for _, change := range changes {
				if change.RecoveryLoss {
					return &toolrejection.ToolReject{Code: "SOURCE_RECOVERY_APPROVAL_UNAVAILABLE", Data: map[string]any{"path": change.Path}}
				}
				if change.Preview.Target == "index" {
					continue
				}
				for _, path := range []string{change.Path, change.FromPath} {
					if path == "" {
						continue
					}
					if _, policy := policyTarget(path); policy {
						return &toolrejection.ToolReject{Code: "FILE_CHANGE_APPROVAL_UNAVAILABLE", Data: map[string]any{"path": path}}
					}
				}
			}
			return nil
		}
		action := hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				NativeInputs:       tc.Invocation.NativeInputs,
				Tool:               tool,
				Args:               args,
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
			},
			Execution: hitl.ActionExecution{
				Contained: hitl.ContainedForRequest(e.actionConfineRequest(ctx, tc)),
			},
		}
		files, policies := map[string]bool{}, map[string]bool{}
		for _, change := range changes {
			action.Mutations.RecoveryLoss = action.Mutations.RecoveryLoss || change.RecoveryLoss
			for _, path := range []string{change.Path, change.FromPath} {
				if path != "" && !files[path] {
					files[path] = true
					action.Invocation.Files = append(action.Invocation.Files, path)
					action.Invocation.NativePathTargets = append(action.Invocation.NativePathTargets, hitl.NativePathTarget{Path: path, Write: true})
					action.Invocation.ResolvedFiles = append(action.Invocation.ResolvedFiles, fspath.CanonicalPath(path))
				}
				if change.Preview.Target == "index" || policies[path] {
					continue
				}
				if target, policy := policyTarget(path); policy {
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
