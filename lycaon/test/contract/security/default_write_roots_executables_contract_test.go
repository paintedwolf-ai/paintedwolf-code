package contract

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// executableInstallLayouts are representative tool homes as installers lay
// them out. The fixture only supplies directories holding executables; the
// check below decides from file modes, not from these names.
var executableInstallLayouts = []string{
	".bun/bin/bun",
	".cargo/bin/cargo",
	".npm/_npx/0a1b2c/node_modules/.bin/create-app",
	".local/bin/tool",
	"go/bin/tool",
	".deno/bin/deno",
}

// packageStoreLayouts hold extracted package archives, which are data.
var packageStoreLayouts = []string{
	".bun/install/cache/left-pad@1.3.0/index.js",
	".npm/_cacache/content-v2/sha512/ab/cd",
	".cargo/registry/cache/index.crates.io/serde-1.0.0.crate",
	".cargo/git/db/example/HEAD",
}

// No default write root is, or contains, a directory holding executables: code
// installed there runs outside the sandbox the next time the person uses it.
func TestDefaultWriteRootsHoldNoExecutableInstallDirectory(t *testing.T) {
	scratch := t.TempDir()
	home := filepath.Join(scratch, "home")
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", filepath.Join(scratch, "tmp"))
	for _, key := range []string{"XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "LYCAON_SANDBOX_WRITE_ROOTS"} {
		t.Setenv(key, "")
	}
	for _, rel := range executableInstallLayouts {
		writeFixture(t, filepath.Join(home, rel), 0o755)
	}
	for _, rel := range packageStoreLayouts {
		writeFixture(t, filepath.Join(home, rel), 0o644)
	}
	canonHome, err := filepath.EvalSymlinks(home)
	contractcheck.FailErr(t, "resolve fixture home", err)

	executableDirs := map[string]bool{}
	err = filepath.WalkDir(canonHome, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			executableDirs[filepath.Dir(path)] = true
		}
		return nil
	})
	contractcheck.FailErr(t, "walk fixture home", err)

	if len(executableDirs) == 0 {
		t.Fatal("no executable directories discovered under fixture home; the check is vacuous")
	}

	underHome := 0
	for _, root := range confine.WriteRootsForProject("", nil) {
		if !within(root, canonHome) {
			continue
		}
		underHome++
		for dir := range executableDirs {
			if within(dir, root) {
				t.Errorf("default write root %s contains executable install directory %s", root, dir)
			}
		}
	}
	if underHome == 0 {
		t.Fatal("no default write root resolved under the fixture home; the check is vacuous")
	}
}

func writeFixture(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	contractcheck.FailErr(t, "create fixture parent", os.MkdirAll(filepath.Dir(path), 0o700))
	contractcheck.FailErr(t, "write fixture", os.WriteFile(path, []byte("fixture\n"), mode))
}

func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimRight(root, "/")+"/")
}
