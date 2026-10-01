package testutil

import "testing"

// SkipIfShort skips when testing.Short() is set: test:short, check-fast, and check.
func SkipIfShort(t testing.TB, reason string) {
	t.Helper()
	if testing.Short() {
		t.Skipf("skipped under -short: %s", reason)
	}
}
