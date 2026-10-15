//go:build linux

package preflight

import (
	"testing"

	"github.com/lycaon/lycaon/internal/dotversion"
	"github.com/lycaon/lycaon/internal/testutil"
)

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

func TestLinuxHostFactsReadTheRealKernelAndLibc(t *testing.T) {
	kernel, err := osProductVersion()
	testutil.FailErr(t, "read kernel release", err)
	if _, err := dotversion.Compare(kernel, "0"); err != nil {
		t.Fatalf("kernel release %q is not a bare dotted version: %v", kernel, err)
	}
	glibc, err := libcVersion()
	testutil.FailErr(t, "read glibc version from the C library", err)
	if cmp, err := dotversion.Compare(glibc, "2.0"); err != nil || cmp < 0 {
		t.Fatalf("glibc version %q is not a plausible dotted version: %v", glibc, err)
	}
}
