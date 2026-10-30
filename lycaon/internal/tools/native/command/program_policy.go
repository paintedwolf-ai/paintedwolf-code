package command

import (
	"context"
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

func (d *ProgramDispatcher) checkPreparedPolicy(ctx context.Context, tc tools.ToolContext, spec toolcontract.CommandSpecification, mode string, request commandprogram.OperationRequest, cwd string, review commandprogram.ArgvReview) error {
	for _, requirement := range spec.Effects {
		if requirement.Mode != selectEffectMode(spec, mode) {
			continue
		}
		effects, err := requirement.CommandEffects(d.childEffectSet(spec, requirement.Mode, request))
		if err != nil {
			return err
		}
		mutation := slices.Contains(effects.Classes(), toolcontract.CommandMutation) || slices.Contains(effects.Classes(), toolcontract.CommandExternalExec)
		targets := []agentpolicy.Target{{Path: cwd, Mutation: mutation}}
		for _, target := range review.PathTargets {
			targets = append(targets, agentpolicy.Target{Path: target.Path, Mutation: target.Write})
		}
		return tc.CheckPolicyTargets(ctx, targets)
	}
	return fmt.Errorf("native policy scope has no catalog effect mode")
}
