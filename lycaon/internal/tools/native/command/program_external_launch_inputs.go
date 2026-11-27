package command

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

func (l ExternalStages) declareLaunchInputs(ctx context.Context, tctx tools.ToolContext, executionID, cwd string, argv []string) (toolcontract.ProducerResult, error) {
	if l.DeclareLaunch == nil {
		return toolcontract.ProducerResult{}, nil
	}
	if err := l.DeclareLaunch(ctx, tctx, executionID, cwd, argv); err != nil {
		if ctx.Err() != nil {
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, errors.Join(ctx.Err(), err)
		}
		return externalLaunchFailure(err, refusedResult("COMMAND_REVISION_CONFLICT")), err
	}
	return toolcontract.ProducerResult{}, nil
}
