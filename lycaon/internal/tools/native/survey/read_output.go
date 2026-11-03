package survey

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// Preserve existing typed refusals and cancellation; storage diagnostics accompany
// a policy-rendered refusal rather than becoming agent-facing recovery advice.
func outputReadError(path string, err error) error {
	if err == nil || toolrejection.AsToolReject(err) != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var coded interface{ MachineErrorCode() string }
	if errors.As(err, &coded) {
		return err
	}
	return errors.Join(&toolrejection.ToolReject{Code: tools.CommandOutputReadUnavailableCode, Data: map[string]any{"path": path}}, err)
}
