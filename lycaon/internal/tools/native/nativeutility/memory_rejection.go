package nativeutility

import (
	"errors"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// ResourceRejection projects the aggregate's actual admission failure without
// resolving a second ceiling or replacing an owner's existing typed rejection.
func ResourceRejection(err error) error {
	if toolrejection.AsToolReject(err) != nil {
		return err
	}
	var memory *toolcontract.ProgramMemoryLimitError
	if !errors.As(err, &memory) {
		return err
	}
	id := ""
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
	return errors.Join(err, LimitExceeded(id, memory.Maximum, memory.Observed))
}
