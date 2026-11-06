package command

import (
	"context"
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func (d *ProgramDispatcher) checkPreparedPolicy(ctx context.Context, tc tools.ToolContext, spec toolcontract.CommandSpecification, mode string, request commandprogram.OperationRequest, cwd projectpaths.Resolved, review commandprogram.ArgvReview) error {
	for _, requirement := range spec.Effects {
		if requirement.Mode != selectEffectMode(spec, mode) {
			continue
		}
		effects, err := requirement.CommandEffects(d.childEffectSet(spec, requirement.Mode, request))
		if err != nil {
			return err
		}
		mutation := slices.Contains(effects.Classes(), toolcontract.CommandMutation) || slices.Contains(effects.Classes(), toolcontract.CommandExternalExec)
		rootID := d.reachedRootID(cwd)
		if cwd.External {
			rootID = ""
		}
		targets := []agentpolicy.Target{{RootID: rootID, Path: cwd.Abs, Mutation: mutation}}
		for _, target := range review.PathTargets {
			targets = append(targets, agentpolicy.Target{Path: target.Path, Mutation: target.Write})
		}
		return tc.CheckPolicyTargets(ctx, targets)
	}
	return fmt.Errorf("native policy scope has no catalog effect mode")
}

func (d *ProgramDispatcher) bindReachedDirectory(request commandprogram.OperationRequest) (commandprogram.OperationRequest, error) {
	cwd := d.paths.Cwd()
	request.Invocation.Scope.Directory = cwd.Abs
	if request.Invocation.Scope.Kind == toolcontract.ProjectOperationScope && !cwd.External && cwd.Root.ID != "" {
		rootID := d.reachedRootID(cwd)
		branch, err := d.run.tc.SourceBranch(rootID)
		if err != nil {
			return request, err
		}
		request.Invocation.Scope.RootID = rootID
		request.Invocation.Scope.BranchID = branch.String()
	}
	return request, nil
}

// reachedRootID names the project root a directory belongs to. A worker branch
// resolves under a synthetic root, so it maps back to the root it mirrors.
func (d *ProgramDispatcher) reachedRootID(cwd projectpaths.Resolved) string {
	return reachedRootID(d.run.tc, cwd)
}

func reachedRootID(tc tools.ToolContext, cwd projectpaths.Resolved) string {
	if tc.Source.WorkerBranchRoot != "" {
		if root, _, ok := tc.SourceLocation(cwd.Abs); ok && root.ID != "" {
			return root.ID
		}
	}
	return cwd.Root.ID
}
