package tools

import (
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/pkg/api"
)

func ownerFailure(tool, owner string, err error) *ToolReject {
	data := map[string]any{"reason": err.Error()}
	var timeout *exec.TimeoutError
	if errors.As(err, &timeout) {
		source := "caller"
		if timeout.CommandDeadline {
			source = "command"
		}
		data["timeout"] = map[string]any{
			"elapsed_ms": timeout.Elapsed.Milliseconds(),
			"deadline":   timeout.Deadline.UTC().Format(time.RFC3339Nano),
			"source":     source,
		}
	}
	return CompleteFailureMetadata(&ToolReject{
		Code: ToolOwnerFailedCode, FailureClass: api.FailureClassOwnerError, OwnerRef: owner,
		Data: data,
	}, tool, owner)
}
