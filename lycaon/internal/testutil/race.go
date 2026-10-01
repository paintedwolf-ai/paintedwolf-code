package testutil

import "testing"

// SkipIfRace skips tests that race instrumentation breaks, not merely slows.
func SkipIfRace(t testing.TB, reason string) {
	t.Helper()
	if raceEnabled {
		t.Skipf("skipped under -race: %s", reason)
	}
}
