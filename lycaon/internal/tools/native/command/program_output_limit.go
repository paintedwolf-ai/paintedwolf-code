package command

import (
	"errors"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

// Delivery limits describe saved bytes; they never rewrite execution outcomes.
func outputDeliveryLimit(err error) *api.CommandProgramOutputLimit {
	var spill *tooloutput.StreamSpillLimitError
	var memory *toolcontract.ProgramMemoryLimitError
	screen := captureprojection.ScreeningLimit(err)
	limit := &api.CommandProgramOutputLimit{Code: "COMMAND_LIMIT_EXCEEDED"}
	switch {
	case errors.As(err, &spill):
		limit.LimitID, limit.Maximum, limit.Observed = "spill_bytes", spill.Maximum, spill.Observed
	case errors.As(err, &memory):
		limit.LimitID, limit.Maximum, limit.Observed = memory.Account, memory.Maximum, memory.Observed
	case screen != nil:
		limit.LimitID, limit.Maximum, limit.Observed = screen.Resource, screen.Maximum, screen.Observed
		if screen.Resource == "screen_output_bytes" || screen.Resource == "screen_input_bytes" {
			limit.LimitID = "spill_bytes"
		}
	default:
		return nil
	}
	return limit
}

func rejectOutputScreenLimit(err error, exit *int, saved []api.CommandProgramOutputWindow) *toolrejection.ToolReject {
	limit := captureprojection.ScreeningLimit(err)
	if limit == nil {
		return nil
	}
	details := map[string]any{"resource": limit.Resource, "maximum": limit.Maximum, "observed": limit.Observed}
	if exit != nil {
		details["command_exit_recorded"] = true
		details["command_exit_status"] = *exit
	}
	var paths []string
	for _, window := range saved {
		if window.OutputPath != "" {
			paths = append(paths, window.OutputPath)
		}
	}
	if len(paths) != 0 {
		details["saved_output_paths"] = paths
	}
	return &toolrejection.ToolReject{Code: "COMMAND_OUTPUT_SCREEN_LIMIT", Data: details}
}
