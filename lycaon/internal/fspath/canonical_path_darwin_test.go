//go:build darwin

package fspath

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestKernelPathDoesNotRequireContentAccess(t *testing.T) {
	if root := os.Getenv("PW_PATH_IDENTITY_FIXTURE"); root != "" {
		for _, path := range []string{root, filepath.Join(root, "file")} {
			fd, err := os.Open(path)
			if err == nil {
				_ = fd.Close()
				t.Fatalf("content access allowed for %s", path)
			}
			got, ok := kernelPath(path)
			if !ok || got != path {
				t.Errorf("kernelPath(%q) = %q, %v", path, got, ok)
			}
		}
		return
	}
	root := filepath.Join(t.TempDir(), "protected")
	testutil.FailErr(t, "create protected directory", os.Mkdir(root, 0o700))
	testutil.FailErr(t, "create protected file", os.WriteFile(filepath.Join(root, "file"), []byte("private"), 0o600))
	root = CanonicalPath(root)
	executable, err := os.Executable()
	testutil.FailErr(t, "locate test executable", err)
	profile := "(version 1)(allow default)(deny file-read-data (subpath " + strconv.Quote(root) + "))"
	cmd := exec.CommandContext(t.Context(), "/usr/bin/sandbox-exec", "-p", profile, executable, "-test.run=^TestKernelPathDoesNotRequireContentAccess$")
	cmd.Env = append(os.Environ(), "PW_PATH_IDENTITY_FIXTURE="+root)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("check metadata-only identity: %v\n%s", err, output)
	}
}
