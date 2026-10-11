package workeradmission

import (
	"fmt"
	"math"

	"github.com/lycaon/lycaon/internal/spawn"
)

// ParseTaskMaxToolLoopsFromArgs reads optional max_tool_loops from task() args.
// Zero means use the configured worker tool-budget default at runtime.
func ParseTaskMaxToolLoopsFromArgs(args map[string]any) (int, error) {
	if args == nil {
		return 0, nil
	}
	raw, ok := args["max_tool_loops"]
	if !ok || raw == nil {
		return 0, nil
	}
	n, err := positiveIntArg(raw)
	if err != nil {
		return 0, fmt.Errorf("max_tool_loops: %w", err)
	}
	return n, nil
}

// ValidateTaskMaxToolLoopsCode returns TASK_MAX_TOOL_LOOPS_INVALID when a non-zero
// requested budget falls outside the configured [Min, Max] bounds. Zero means omit.
func ValidateTaskMaxToolLoopsCode(requested int, budget spawn.WorkerToolBudget) string {
	if requested <= 0 {
		return ""
	}
	if requested < budget.Min || requested > budget.Max {
		return TaskMaxToolLoopsInvalidCode
	}
	return ""
}

func positiveIntArg(raw any) (int, error) {
	switch v := raw.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
			return 0, fmt.Errorf("must be a positive integer")
		}
		n := int(v)
		if n <= 0 {
			return 0, fmt.Errorf("must be a positive integer")
		}
		return n, nil
	case int:
		if v <= 0 {
			return 0, fmt.Errorf("must be a positive integer")
		}
		return v, nil
	case int64:
		if v <= 0 || v > math.MaxInt {
			return 0, fmt.Errorf("must be a positive integer")
		}
		return int(v), nil
	default:
		return 0, fmt.Errorf("must be a positive integer")
	}
}
