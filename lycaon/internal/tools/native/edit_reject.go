package native

import "github.com/lycaon/lycaon/internal/toolrejection"

// EditArgsConflict refuses edit arguments that cannot name one replacement.
func EditArgsConflict(detail string) error {
	return &toolrejection.ToolReject{
		Code: "EDIT_ARGS_CONFLICT",
		Data: map[string]any{"detail": detail, "conflict": true},
	}
}
