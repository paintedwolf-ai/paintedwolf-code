package command

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// sleepOperation waits for the sum of its bound durations.
func sleepOperation(spec toolcontract.CommandSpecification) NativeOperation {
	return preparedHostBuiltin(spec, func(request commandprogram.OperationRequest, bound commandprogram.BoundArgv) (hostedBody, error) {
		operands := bound.Operands().Values()
		if len(operands) == 0 {
			return nil, toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "sleep", "reason": "requires a duration"})
		}
		var total time.Duration
		for _, operand := range operands {
			duration, ok := commandprogram.ParseDuration(operand)
			if !ok {
				return nil, toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "sleep", "operand": operand, "reason": "a duration such as 5, 0.5, 2m or 1h"})
			}
			if duration > time.Duration(1<<63-1)-total {
				return nil, toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "sleep", "operand": operand})
			}
			total += duration
		}
		statement := strings.Join(request.Argv.Values(), " ")
		return func(ctx context.Context, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
			if err := request.Wait.Until(ctx, request.Wait.Now().Add(total), statement); err != nil {
				if reject := toolrejection.AsToolReject(err); reject != nil {
					return refusedResult(reject.Code), err
				}
				return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, StopCode: toolrejection.ToolOwnerInterruptedCode, EffectCertainty: toolcontract.NoEffect}, err
			}
			return readResult(0), nil
		}, nil
	})
}
