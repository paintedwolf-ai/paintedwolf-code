package worker

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// skipWithoutMount turns on confinement, which a mounted view requires, and skips on a host with no
// way to mount a view. Any other failure to mount is left for the test to report.
func skipWithoutMount(t *testing.T, selector *adapter.Selector) {
	t.Helper()
	confine.TestingSetAutoConfine(t)
	if !confine.Available() {
		t.Skip("this host applies no confinement")
	}
	_, err := selector.Choose(t.Context(), adapter.ChooseRequest{
		Probe:  adapter.ProbeConfig{RuntimeDir: t.TempDir()},
		Grants: []adapter.RootGrant{{RootID: "probe", Label: "probe", SourcePath: t.TempDir(), IsPrimary: true}},
	})
	var adapterErr *adapter.AdapterError
	if !errors.As(err, &adapterErr) {
		return
	}
	switch adapterErr.ReasonCode {
	case adapter.ReasonFUSEDeviceUnavailable, adapter.ReasonFUSEHelperUnavailable, adapter.ReasonUnsupportedPlatform:
		t.Skipf("this host cannot mount a view: %s", adapterErr.ReasonCode)
	default:
	}
}
