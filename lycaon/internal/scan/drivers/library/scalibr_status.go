package library

import (
	"fmt"
	"strings"

	"github.com/google/osv-scalibr/plugin"
	scalibrresult "github.com/google/osv-scalibr/result"
)

type scalibrPluginFailure struct {
	name   string
	status int
	reason string
}

func (f *scalibrPluginFailure) Error() string {
	return fmt.Sprintf("scalibr plugin %q did not complete: status=%d reason=%s", f.name, f.status, f.reason)
}

func failedScalibrPlugin(states []*plugin.Status) error {
	for _, state := range states {
		if state != nil && state.Status != nil && state.Status.Status == plugin.ScanStatusSucceeded {
			continue
		}
		failure := &scalibrPluginFailure{name: "unknown", status: int(plugin.ScanStatusUnspecified)}
		if state != nil {
			failure.name = state.Name
			if state.Status != nil {
				failure.status = int(state.Status.Status)
				failure.reason = strings.TrimSpace(state.Status.FailureReason)
			}
		}
		return failure
	}
	return nil
}

func validateScalibrResult(result *scalibrresult.ScanResult) error {
	if result == nil || result.Status == nil {
		return fmt.Errorf("scalibr scan returned no status")
	}
	failure := failedScalibrPlugin(result.PluginStatus)
	if result.Status.Status != plugin.ScanStatusSucceeded {
		aggregate := fmt.Sprintf("scalibr scan did not complete: status=%d reason=%s", result.Status.Status, strings.TrimSpace(result.Status.FailureReason))
		if failure != nil {
			return fmt.Errorf("%s: %w", aggregate, failure)
		}
		return fmt.Errorf("%s", aggregate)
	}
	return failure
}
