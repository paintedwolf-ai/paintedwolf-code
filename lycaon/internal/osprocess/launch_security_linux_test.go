//go:build linux

package osprocess

import (
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

const fixtureSecurityStatus = "Uid:\t1000 1000 1000 1000\nGid:\t1000 1000 1000 1000\nCapPrm:\t1\nCapEff:\t1\nCapAmb:\t0\nCapInh:\t2\nCapBnd:\t3\nNoNewPrivs:\t0\n"

func TestLinuxSecurityObservationPreservesCapabilityInputs(t *testing.T) {
	facts, err := parseProcessSecurity([]byte(fixtureSecurityStatus))
	testutil.FailErr(t, "parse synthetic credential facts", err)
	if facts.Linux.Inheritable != 2 || facts.Linux.Bounding != 3 || facts.Linux.Permitted != 1 {
		t.Fatal("capability transition inputs lost")
	}
	for _, bad := range []string{strings.Replace(fixtureSecurityStatus, "CapInh:\t2\n", "", 1), strings.Replace(fixtureSecurityStatus, "CapBnd:\t3", "CapBnd:\tinvalid", 1)} {
		if _, err := parseProcessSecurity([]byte(bad)); err == nil {
			t.Fatal("missing/malformed capability facts reported as zero")
		}
	}
}
func TestLinuxSecurityObservationMatchesActualInheritableCapabilities(t *testing.T) {
	facts, err := ProcessCredentials(os.Getpid())
	testutil.FailErr(t, "observe native own credentials", err)
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	testutil.FailErr(t, "observe actual capability words", unix.Capget(&header, &data[0]))
	inherited := uint64(data[0].Inheritable) | uint64(data[1].Inheritable)<<32
	if facts.Linux == nil || facts.Linux.Inheritable != inherited {
		t.Fatal("proc observation disagrees with native capability call")
	}
}
