package toolrejection

import (
	"errors"

	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/gitexec"
)

// GitFailureObservation preserves host-defined rejection types through tool adapters.
// Ordinary process diagnostics remain owner failures; no text is classified.
func GitFailureObservation(err error) *ToolReject {
	var unsafe *gitexec.UnsafeRepoConfigError
	if errors.As(err, &unsafe) {
		return &ToolReject{Code: unsafe.Code(), Data: map[string]any{"keys": append([]string(nil), unsafe.Keys...)}}
	}
	var signing *gitexec.SigningUnsupportedError
	if errors.As(err, &signing) {
		return &ToolReject{Code: signing.Code(), Data: map[string]any{"reason": signing.Detail}}
	}
	var unavailable *gitengine.UnavailableError
	if errors.As(err, &unavailable) {
		return &ToolReject{Code: "GIT_ENGINE_UNAVAILABLE", Data: map[string]any{"reason": unavailable.Reason}}
	}
	return nil
}
