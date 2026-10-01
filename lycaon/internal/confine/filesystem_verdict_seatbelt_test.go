package confine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The kernel and the host verdict read one rule list; every probe must get the
// same answer from both, or a denied effect could go unreported.
func TestSeatbeltAgreesWithFilesystemVerdict(t *testing.T) {
	self := requireSeatbelt(t)
	c, probes := verdictFixture(t, outsideTemporaryWriteRoots(t))
	for _, probe := range probes {
		switch {
		case probe.exists:
			writeFixtureFile(t, probe.path)
		case probe.path != "/dev/disk0" && probe.path != "/dev/null":
			// A missing parent would refuse the write for a reason other than the boundary.
			testutil.FailErr(t, "create probe parent", os.MkdirAll(filepath.Dir(probe.path), 0o700))
		}
	}
	boundary := confine.BoundaryOf(&c)
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			verdict := boundary.Filesystem.Verdict(probe.access, probe.path)
			kernelAllowed := kernelPermits(t, self, c, probe)
			if kernelAllowed != verdict.Allowed {
				t.Fatalf("kernel allowed=%v, verdict=%+v for %s %s", kernelAllowed, verdict, probe.access, probe.path)
			}
		})
	}
}

func kernelPermits(t *testing.T, self string, c confine.Confinement, probe boundaryProbe) bool {
	t.Helper()
	if probe.access == confine.AccessRead {
		return confinedExit(t, self, c, "/bin/cat", probe.path) == 0
	}
	if probe.path == "/dev/disk0" {
		// Opening the device for write proves the rule without writing a byte.
		return confinedExit(t, self, c, "/bin/sh", "-c", `exec 3>>"$1"`, "sh", probe.path) == 0
	}
	_, statErr := os.Lstat(probe.path)
	code := confinedExit(t, self, c, "/bin/sh", "-c", `: >> "$1"`, "sh", probe.path)
	if code == 0 && os.IsNotExist(statErr) {
		_ = os.Remove(probe.path)
	}
	return code == 0
}
