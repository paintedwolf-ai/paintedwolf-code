//go:build linux

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The boundary BundledScannerConfinement compiles keeps the project read-only
// and the output folder writable. A fixture process stands in for the scanner;
// installed-package qualification covers the shipped scanner's own bytes.
func TestLinuxScannerBoundaryKeepsSourceReadOnlyAndOutputWritable(t *testing.T) {
	if !confine.Available() {
		caps, _ := confine.LinuxCapabilities()
		t.Fatalf("required Linux scanner boundary is unavailable: %+v", caps)
	}
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	python, err := exec.LookPath("python3")
	testutil.FailErr(t, "locate scanner fixture runtime", err)
	project, output := t.TempDir(), t.TempDir()
	input := filepath.Join(project, "input")
	result := filepath.Join(output, "result")
	testutil.FailErr(t, "write scanner input", os.WriteFile(input, []byte("source"), 0o600))
	box, err := scan.BundledScannerConfinement(project, output)
	testutil.FailErr(t, "prepare scanner boundary", err)
	if box == nil {
		t.Fatal("required scanner boundary was omitted")
	}
	const script = `import errno, sys
assert open(sys.argv[1]).read() == 'source'
open(sys.argv[2], 'w').write('result')
try:
    open(sys.argv[1], 'w').write('escaped')
except OSError as error:
    assert error.errno in (errno.EACCES, errno.EROFS), error
else:
    raise RuntimeError('scanner process changed its read-only input')
`
	self, err := os.Executable()
	testutil.FailErr(t, "scanner helper executable", err)
	cmd, cleanup, err := confine.Command(t.Context(), self, python, []string{"-I", "-c", script, input, result}, *box)
	testutil.FailErr(t, "confined scanner fixture", err)
	defer cleanup()
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("applied scanner fixture: %v: %s", err, data)
	}
	data, err := os.ReadFile(result)
	testutil.FailErr(t, "read scanner output", err)
	if string(data) != "result" {
		t.Fatalf("scanner output = %q", data)
	}
	data, err = os.ReadFile(input)
	testutil.FailErr(t, "read preserved scanner input", err)
	if string(data) != "source" {
		t.Fatalf("scanner changed read-only input: %q", data)
	}
}
