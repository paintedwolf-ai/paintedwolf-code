package confine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The kernel and the host verdict read one rule list; every probe must get the
// same answer from both, or a denied effect could go unreported.
func TestKernelAgreesWithFilesystemVerdict(t *testing.T) {
	helper := requireLiveBoundary(t)
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
	permitted := kernelPermits(t, helper, c, probes)
	for i, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			verdict := boundary.Filesystem.Verdict(probe.access, probe.path)
			if permitted[i] != verdict.Allowed {
				t.Fatalf("kernel allowed=%v, verdict=%+v for %s %s", permitted[i], verdict, probe.access, probe.path)
			}
		})
	}
}

// kernelProbeScript answers each (access, path) argument pair with 1 when the
// kernel permits it and 0 when it does not, one line per pair.
const kernelProbeScript = `while [ "$#" -gt 1 ]; do
	case "$1" in
	read) /bin/cat -- "$2" >/dev/null 2>&1 ;;
	device) (exec 3>>"$2") 2>/dev/null ;;
	*) (: >>"$2") 2>/dev/null ;;
	esac && echo 1 || echo 0
	shift 2
done`

// kernelPermits asks the kernel about every probe from one confined launch.
// Opening a device for write proves the rule without writing a byte, and the
// paths are distinct, so a file one probe creates cannot answer another.
func kernelPermits(t *testing.T, helper string, c confine.Confinement, probes []boundaryProbe) []bool {
	t.Helper()
	args := []string{"-c", kernelProbeScript, "sh"}
	absent := map[string]bool{}
	for _, probe := range probes {
		kind := "write"
		switch {
		case probe.access == confine.AccessRead:
			kind = "read"
		case probe.path == "/dev/disk0":
			kind = "device"
		}
		if _, err := os.Lstat(probe.path); os.IsNotExist(err) {
			absent[probe.path] = true
		}
		args = append(args, kind, probe.path)
	}
	code, out, stderr := confinedStdout(t, helper, c, "/bin/sh", args...)
	for path := range absent {
		_ = os.Remove(path)
	}
	answers := strings.Fields(out)
	if code != 0 || len(answers) != len(probes) {
		t.Fatalf("kernel probe: exit=%d, %d answers for %d probes\nstdout:\n%s\nstderr:\n%s", code, len(answers), len(probes), out, stderr)
	}
	permitted := make([]bool, len(probes))
	for i, answer := range answers {
		permitted[i] = answer == "1"
	}
	return permitted
}
