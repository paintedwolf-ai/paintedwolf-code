package command

import (
	"errors"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

// Delivery limits describe saved bytes; they never rewrite execution outcomes.
func outputDeliveryLimit(err error) *api.CommandProgramOutputLimit {
	var spill *tooloutput.StreamSpillLimitError
	var memory *toolcontract.ProgramMemoryLimitError
	var screen *secretmatch.StreamLimitError
	limit := &api.CommandProgramOutputLimit{Code: "COMMAND_LIMIT_EXCEEDED"}
	switch {
	case errors.As(err, &spill):
		limit.LimitID, limit.Maximum, limit.Observed = "spill_bytes", spill.Maximum, spill.Observed
	case errors.As(err, &memory):
		limit.LimitID, limit.Maximum, limit.Observed = memory.Account, memory.Maximum, memory.Observed
	case errors.As(err, &screen):
		limit.LimitID, limit.Maximum, limit.Observed = screen.Resource, screen.Maximum, screen.Observed
		if screen.Resource == "screen_output_bytes" || screen.Resource == "screen_input_bytes" {
			limit.LimitID = "spill_bytes"
		}
	default:
		return nil
	}
	return limit
}
