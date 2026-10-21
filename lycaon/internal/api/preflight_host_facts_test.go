package api

import (
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/platformfloor"
	"github.com/lycaon/lycaon/internal/preflight"
)

// The support facts pair the running OS version with the floor it is compared
// against: the kernel on Linux, macOS on a Mac.
func TestHostFactsReportTheFloorTheHostIsComparedAgainst(t *testing.T) {
	facts := preflightHostFacts(preflight.Env{OSProductVer: func() (string, error) { return "6.8.0", nil }})
	if facts.OSVersion != "6.8.0" || facts.OSName != runtime.GOOS {
		t.Fatalf("host facts = %+v", facts)
	}
	want := map[string]string{"linux": platformfloor.LinuxKernelMin(), "darwin": platformfloor.MacOSMin()}[runtime.GOOS]
	if facts.OSFloor != platformfloor.HostOSMin() || (want != "" && facts.OSFloor != want) {
		t.Fatalf("os floor = %q, want %q", facts.OSFloor, want)
	}
}
