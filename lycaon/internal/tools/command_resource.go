package tools

import (
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// commandResourceUnit returns the catalog unit for a resource limit id.
func commandResourceUnit(id string) string {
	if catalog, err := toolcontract.LanguageV1(); err == nil {
		for _, limit := range catalog.Language.Limits {
			if limit.ID == id && limit.Unit != "" {
				return limit.Unit
			}
		}
	}
	if strings.HasSuffix(id, "_bytes") {
		return "bytes"
	}
	return "items"
}

// CommandLimitExceeded reports the reached catalog limit and actual counters.
func CommandLimitExceeded(id string, maximum, observed int64) error {
	return &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": id, "resource_unit": commandResourceUnit(id), "maximum": maximum, "observed": observed}}
}

// CommandResourceRejection projects actual evaluator and aggregate limits without
// resolving a second ceiling or replacing an owner's existing typed rejection.
func CommandResourceRejection(err error) error {
	if toolrejection.AsToolReject(err) != nil {
		return err
	}
	var program *commandprogram.ProgramLimitError
	if errors.As(err, &program) {
		return errors.Join(err, CommandLimitExceeded(program.Limit, program.Maximum, program.Observed))
	}
	var memory *toolcontract.ProgramMemoryLimitError
	if !errors.As(err, &memory) {
		return err
	}
	var id string
	switch memory.Account {
	case "working":
		id = "evaluation_memory_bytes"
	case "state":
		id = "program_state_bytes"
	case "session_spill_bytes":
		id = memory.Account
	default:
		return err
	}
	rejection := CommandLimitExceeded(id, memory.Maximum, memory.Observed)
	if reject := toolrejection.AsToolReject(rejection); reject != nil {
		if memory.Requested > 0 {
			reject.Data["held_bytes"], reject.Data["requested_bytes"] = memory.Held, memory.Requested
		}
		if memory.Phase != "" {
			reject.Data["resource_phase"] = string(memory.Phase)
		}
	}
	return errors.Join(err, rejection)
}
