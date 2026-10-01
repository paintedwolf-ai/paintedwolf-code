package wiring

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil/extstatetest"
)

// installHarnessPacks links (kind: path) each author folder into the harness's
// isolated extension cache, so failures point at the fixture file itself.
func installHarnessPacks(t *testing.T, dirs []string) {
	t.Helper()
	for _, dir := range dirs {
		extstatetest.InstallPack(t, extstatetest.DeviceScope(), "path:"+dir, "", "")
	}
}
