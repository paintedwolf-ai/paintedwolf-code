//go:build darwin || linux

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/admissiontest"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestBundledScannerConfinementProtectsEngineRuntimeAndAppCaches verifies under
// real OS confinement (Seatbelt on macOS, Landlock on Linux) that a scan worker
// can read and execute its host-prepared runtime and write only to its own
// ActionWorkspace, while writes to the prepared engine runtime, unrelated app
// caches under UserConfigDir, and the project tree are all kernel-denied.
func TestBundledScannerConfinementProtectsEngineRuntimeAndAppCaches(t *testing.T) {
	testutil.RequireConfinement(t, confine.Available())
	admissiontest.Enable(t)
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")

	python, err := exec.LookPath("python3")
	testutil.FailErr(t, "locate python3 fixture runtime", err)

	cfgDir, err := configdir.UserConfigDir()
	testutil.FailErr(t, "resolve user config dir", err)

	runtimeStore := enginepaths.OpengrepRuntimeRootUnder(cfgDir)
	testutil.FailErr(t, "create runtime store", os.MkdirAll(runtimeStore, 0o700))
	runtimeRoot, err := os.MkdirTemp(runtimeStore, "v1-test-runtime-")
	testutil.FailErr(t, "create runtime fixture root", err)
	runtimeCache := filepath.Join(runtimeRoot, "xdg-cache")
	testutil.FailErr(t, "create runtime xdg-cache", os.MkdirAll(runtimeCache, 0o700))
	engineHelper := filepath.Join(runtimeCache, "helper.sh")
	testutil.FailErr(t, "write engine helper", os.WriteFile(engineHelper, []byte("#!/bin/sh\nprintf 'engine-ok'\n"), 0o555))
	testutil.FailErr(t, "seal runtime xdg-cache", os.Chmod(runtimeCache, 0o555))
	testutil.FailErr(t, "seal runtime root", os.Chmod(runtimeRoot, 0o555))
	t.Cleanup(func() {
		_ = os.Chmod(runtimeRoot, 0o700)
		_ = os.Chmod(runtimeCache, 0o700)
		_ = os.Chmod(engineHelper, 0o600)
		_ = os.RemoveAll(runtimeRoot)
	})

	unrelatedCache := filepath.Join(enginepaths.SourceCatalogCacheRootUnder(cfgDir), "nr12-unrelated-test")
	testutil.FailErr(t, "create unrelated app cache", os.MkdirAll(unrelatedCache, 0o700))
	t.Cleanup(func() { _ = os.RemoveAll(unrelatedCache) })

	workspace, err := confine.NewActionWorkspace()
	testutil.FailErr(t, "create scanner workspace", err)
	t.Cleanup(func() { _ = os.RemoveAll(workspace.Path()) })

	project := t.TempDir()
	projectFile := filepath.Join(project, "input.txt")
	testutil.FailErr(t, "write project input", os.WriteFile(projectFile, []byte("source"), 0o600))

	box, err := scan.BundledScannerConfinement(project, workspace, runtimeCache)
	testutil.FailErr(t, "prepare scanner confinement", err)
	if box == nil {
		t.Fatal("required scanner confinement was omitted")
	}
	if len(box.GrantedWriteRoots) != 0 || len(box.Roots) != 0 {
		t.Fatalf("unexpected write roots: roots=%v granted=%v", box.Roots, box.GrantedWriteRoots)
	}

	const script = `import errno, os, subprocess, sys
project_file, engine_helper, runtime_cache, unrelated_cache, workspace = sys.argv[1:6]

# 1. Allowed: read project, read+exec prepared engine runtime, write action workspace.
assert open(project_file).read() == 'source'
assert subprocess.check_output([engine_helper]).decode() == 'engine-ok'
out = os.path.join(workspace, 'report.json')
open(out, 'w').write('ok')

# 2. Denied: write to prepared engine runtime, chmod engine helper, write unrelated app cache, write project.
def assert_denied(label, fn, allow_hidden=False):
    allowed = (errno.EACCES, errno.EPERM, errno.EROFS, errno.ENOENT) if allow_hidden else (errno.EACCES, errno.EPERM, errno.EROFS)
    try:
        fn()
    except OSError as err:
        assert err.errno in allowed, (label, err)
    else:
        raise RuntimeError(f'{label}: unexpectedly succeeded')

assert_denied('create in runtime cache', lambda: open(os.path.join(runtime_cache, 'pwn'), 'w').write('x'))
assert_denied('chmod engine helper', lambda: os.chmod(engine_helper, 0o700))
assert_denied('write unrelated app cache', lambda: open(os.path.join(unrelated_cache, 'pwn'), 'w').write('x'), allow_hidden=True)
assert_denied('write project tree', lambda: open(project_file, 'w').write('pwn'))
`
	self, err := os.Executable()
	testutil.FailErr(t, "scanner helper executable", err)
	cmd, cleanup, err := confine.Command(
		t.Context(), self, python,
		[]string{"-I", "-c", script, projectFile, engineHelper, runtimeCache, unrelatedCache, workspace.Path()},
		*box,
	)
	testutil.FailErr(t, "confined scanner runtime test", err)
	defer cleanup()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("confined scanner runtime check failed: %v\n%s", err, out)
	}
}
