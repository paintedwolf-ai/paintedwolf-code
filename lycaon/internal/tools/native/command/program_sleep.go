package command

import (
	"context"
	"math"
	"strconv"
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
			duration, ok := sleepDuration(operand)
			if !ok {
				return refusedResult("COMMAND_OPERAND_INVALID"), toolrejection.RejectInvalidArguments("COMMAND_OPERAND_INVALID", map[string]any{"command": "sleep", "operand": operand, "reason": "a duration such as 5, 0.5, 2m or 1h"})
			}
			total += duration
		}
		timer := time.NewTimer(total)
		defer timer.Stop()
		select {
		case <-timer.C:
			return readResult(0), nil
		case <-ctx.Done():
			return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, StopCode: toolrejection.ToolOwnerInterruptedCode, EffectCertainty: toolcontract.NoEffect}, ctx.Err()
		}
	})
}

func sleepDuration(operand string) (time.Duration, bool) {
	unit := time.Second
	switch {
	case strings.HasSuffix(operand, "s"):
		operand = strings.TrimSuffix(operand, "s")
	case strings.HasSuffix(operand, "m"):
		operand, unit = strings.TrimSuffix(operand, "m"), time.Minute
	case strings.HasSuffix(operand, "h"):
		operand, unit = strings.TrimSuffix(operand, "h"), time.Hour
	case strings.HasSuffix(operand, "d"):
		operand, unit = strings.TrimSuffix(operand, "d"), 24*time.Hour
	}
	value, err := strconv.ParseFloat(operand, 64)
	if err != nil || value < 0 || math.IsInf(value, 0) || math.IsNaN(value) || strings.ContainsAny(operand, "eEnN+") {
		return 0, false
	}
	scaled := value * float64(unit)
	if scaled > float64(math.MaxInt64) {
		return 0, false
	}
	return time.Duration(scaled), true
}
