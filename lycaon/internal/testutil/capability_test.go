package testutil

import "testing"

type capabilityVerdict struct {
	testing.TB
	failed, skipped bool
}

func (v *capabilityVerdict) Helper()               {}
func (v *capabilityVerdict) Fatalf(string, ...any) { v.failed = true }
func (v *capabilityVerdict) Skipf(string, ...any)  { v.skipped = true }

func TestRequiredCapabilityFailsAndOptionalSkipsWhereAbsent(t *testing.T) {
	for _, require := range []func(testing.TB, bool){RequireConfinement, RequireLineage} {
		for _, tc := range []struct {
			present         bool
			promise         string
			failed, skipped bool
		}{
			{present: true, promise: "1"},
			{present: true},
			{present: false, promise: "1", failed: true},
			{present: false, skipped: true},
			{present: false, promise: "0", skipped: true},
		} {
			t.Setenv(EnvConfineRequired, tc.promise)
			verdict := &capabilityVerdict{TB: t}
			require(verdict, tc.present)
			if verdict.failed != tc.failed || verdict.skipped != tc.skipped {
				t.Fatalf("present=%v %s=%q: failed=%v skipped=%v, want failed=%v skipped=%v",
					tc.present, EnvConfineRequired, tc.promise, verdict.failed, verdict.skipped, tc.failed, tc.skipped)
			}
		}
	}
}
