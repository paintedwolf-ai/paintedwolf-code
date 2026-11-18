package integration

import (
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/testutil"
)

const stagedGitFixture = "PW_TEST_STAGED_GIT"

// stagedInstallation runs this contract with its own version-checked installation.
func stagedInstallation(t *testing.T, withLFS bool) bool {
	t.Helper()
	if os.Getenv(stagedGitFixture) == t.Name() {
		return false
	}
	requireBundledEngine(t)
	bin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "resolve Git", err)
	lfs, err := gitengine.LFSPath()
	testutil.FailErr(t, "resolve LFS", err)
	source := filepath.Dir(filepath.Dir(bin))
	destination := filepath.Join(t.TempDir(), "Painted Wolf Code.app")
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if path == lfs && !withLFS {
			return nil
		}
		if path == bin || path == lfs {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, 0o755)
		}
		return os.Symlink(path, target)
	})
	testutil.FailErr(t, "stage Git installation", err)
	relative, err := filepath.Rel(source, bin)
	testutil.FailErr(t, "locate staged Git", err)
	executable, err := os.Executable()
	testutil.FailErr(t, "locate fixture process", err)
	cmd := osexec.CommandContext(t.Context(), executable, "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.v")
	cmd.Env = append(os.Environ(), stagedGitFixture+"="+t.Name(), gitengine.EnvTest+"=1", gitengine.EnvGitBinary+"="+filepath.Join(destination, relative))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("staged Git contract: %v\n%s", err, output)
	}
	return true
}
