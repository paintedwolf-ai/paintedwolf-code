package command

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// sleepBuiltin waits for the sum of its durations: decimal seconds with an
// optional s, m, h or d suffix. Cancellation and the program deadline end it.
func sleepBuiltin() commandprogram.OperationDispatcher {
	return jobBuiltin(func(ctx context.Context, request commandprogram.OperationRequest, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
		operands := request.Argv.Values()[1:]
		if len(operands) > 0 && operands[0] == "--" {
			operands = operands[1:]
		}
		if len(operands) == 0 {
			return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "sleep", "reason": "requires a duration"})
		}
		var total time.Duration
		for _, operand := range operands {
			duration, ok := commandprogram.ParseDuration(operand)
			if !ok {
				return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "sleep", "operand": operand, "reason": "a duration such as 5, 0.5, 2m or 1h"})
			}
			if duration > time.Duration(1<<63-1)-total {
				return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "sleep", "operand": operand})
			}
			total += duration
		}
		if err := request.Wait.Until(ctx, request.Wait.Now().Add(total), strings.Join(request.Argv.Values(), " ")); err != nil {
			if reject := toolrejection.AsToolReject(err); reject != nil {
				return refusedResult(reject.Code), err
			}
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, StopCode: toolrejection.ToolOwnerInterruptedCode, EffectCertainty: toolcontract.NoEffect}, err
		}
		return readResult(0), nil
	})
}
