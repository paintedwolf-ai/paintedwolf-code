//go:build !linux

package confine_test

import (
	"os"
	"os/exec"
	"runtime"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// requireLiveBoundary returns the helper that applies the host's real boundary.
// Applying Seatbelt is slow enough to stay out of short runs.
func requireLiveBoundary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" || testing.Short() || !confine.Available() {
		t.Skip("darwin + seatbelt sandbox required")
	}
	warmPythonShim.Do(func() {
		// /usr/bin/python3 is an xcrun shim that caches under the Darwin user temp
		// directory, outside the isolated TMPDIR that confined children may write.
		if out, err := exec.Command("/usr/bin/python3", "-c", "pass").CombinedOutput(); err != nil {
			t.Logf("warm python3 shim: %v: %s", err, out)
		}
	})
	self, err := os.Executable()
	testutil.FailErr(t, "os.Executable failed", err)
	return self
}

var warmPythonShim sync.Once

// liveBoundaryRoots adds what a confined helper needs beside roots.
func liveBoundaryRoots(roots ...string) []string { return roots }
