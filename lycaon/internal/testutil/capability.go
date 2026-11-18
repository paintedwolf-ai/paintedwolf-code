package testutil

import (
	"os"
	"testing"
)

// EnvConfineRequired makes a host without confinement or process lineage
// fatal instead of a skip. Lanes whose hosts provide both set it, so a test
// that needs them cannot pass by covering nothing.
const EnvConfineRequired = "PW_CONFINE_REQUIRED"

// RequireConfinement skips the test where the host applies no confinement
// (confine.Available), and fails it where confinement was promised.
func RequireConfinement(t testing.TB, available bool) {
	t.Helper()
	requireCapability(t, available, "confinement")
}

// RequireLineage skips the test where the host cannot observe process lineage
// (lineage.Supported), and fails it where lineage was promised.
func RequireLineage(t testing.TB, supported bool) {
	t.Helper()
	requireCapability(t, supported, "process lineage")
}

func requireCapability(t testing.TB, present bool, capability string) {
	t.Helper()
	if present {
		return
	}
	if os.Getenv(EnvConfineRequired) == "1" {
		t.Fatalf("this host provides no %s, but %s=1 promises it", capability, EnvConfineRequired)
		return
	}
	t.Skipf("this host provides no %s", capability)
}
