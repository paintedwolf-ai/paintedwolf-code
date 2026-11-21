package search

import (
	"context"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// Invoke prepares and runs one request, settling a binding refusal as the
// producer outcome so callers without a separate review step see one result.
func (o *Owners) Invoke(ctx context.Context, tc tools.ToolContext, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	prepared, err := o.Prepare(paths, dispatch, request)
	if err != nil {
		result := toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
		if reject := toolrejection.AsToolReject(err); reject != nil {
			result.Terminal, result.StopCode = toolcontract.ProducerRefused, reject.Code
		}
		return result, err
	}
	return prepared.InvokeOwner(ctx, tc, streams)
}
