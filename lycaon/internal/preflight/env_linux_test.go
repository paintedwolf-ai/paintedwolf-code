//go:build linux

package preflight

import "testing"

func TestLinuxDefaultEnvReportsHealthyHostFacts(t *testing.T) {
	env := DefaultEnv(t.TempDir(), nil, nil, nil, nil, nil)
	if got := run(t, osVersionProbe{}, env); got.Status != StatusOK || got.Code != "" {
		t.Fatalf("os version probe = %+v, want ok without a code", got)
	}
	free, err := env.FreeBytes(env.ConfigDir)
	if err != nil || free == 0 {
		t.Fatalf("free bytes = %d, %v; want a statfs reading", free, err)
	}
	env.FreeBytes = func(string) (uint64, error) { return diskSpaceFloorBytes - 1, nil }
	if got := run(t, diskSpaceProbe{}, env); got.Code != CodeDiskSpaceLow {
		t.Fatalf("disk space probe code = %q, want %q", got.Code, CodeDiskSpaceLow)
	}
}
