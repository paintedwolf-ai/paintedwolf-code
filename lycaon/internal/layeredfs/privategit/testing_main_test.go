package privategit

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}

type repoFixture struct {
	tmpDir      string
	trunkDir    string
	subSource   string
	linkedWTDir string
}

// buildRichRepoFixture creates a packed trunk repository with a submodule, a linked worktree,
// a clean committed file, a staged modification, and an unstaged modification.
func buildRichRepoFixture(t *testing.T) repoFixture {
	t.Helper()
	tmp := t.TempDir()

	// 1. Create and commit submodule source repo.
	subSrc := filepath.Join(tmp, "sub-origin")
	testutil.FailErr(t, "mkdir sub-origin", os.MkdirAll(subSrc, 0o755))
	gittest.Init(t, subSrc)
	testutil.FailErr(t, "write sub lib.txt", os.WriteFile(filepath.Join(subSrc, "lib.txt"), []byte("sub-v1\n"), 0o644))
	gittest.CommitAll(t, subSrc, "sub initial commit")
	gittest.Run(t, subSrc, "repack", "-a", "-d")

	// 2. Create trunk repository with multiple commits, a branch, a submodule, and packed objects.
	trunk := filepath.Join(tmp, "trunk")
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
	gittest.Init(t, trunk)
	gittest.Run(t, trunk, "checkout", "-b", "main")

	testutil.FailErr(t, "write committed.txt", os.WriteFile(filepath.Join(trunk, "committed.txt"), []byte("committed-v1\n"), 0o644))
	testutil.FailErr(t, "write staged.txt", os.WriteFile(filepath.Join(trunk, "staged.txt"), []byte("staged-base\n"), 0o644))
	testutil.FailErr(t, "write unstaged.txt", os.WriteFile(filepath.Join(trunk, "unstaged.txt"), []byte("unstaged-base\n"), 0o644))
	gittest.CommitAll(t, trunk, "initial trunk commit")

	testutil.FailErr(t, "update committed.txt v2", os.WriteFile(filepath.Join(trunk, "committed.txt"), []byte("committed-v2\n"), 0o644))
	gittest.CommitAll(t, trunk, "second trunk commit")

	// Add submodule at `sub`.
	gittest.Run(t, trunk, "-c", "protocol.file.allow=always", "submodule", "add", subSrc, "sub")
	gittest.CommitAll(t, trunk, "add submodule sub")

	// Create a secondary branch for switch/checkout testing and a linked worktree.
	gittest.Run(t, trunk, "branch", "side-branch")
	gittest.Run(t, trunk, "branch", "wt-branch")
	linkedWT := filepath.Join(tmp, "linked-wt")
	gittest.Run(t, trunk, "worktree", "add", linkedWT, "wt-branch")

	// Pack objects in trunk so the fixture is a packed repository.
	gittest.Run(t, trunk, "repack", "-a", "-d")

	// Create pre-existing staged and unstaged changes in trunk.
	testutil.FailErr(t, "modify staged.txt", os.WriteFile(filepath.Join(trunk, "staged.txt"), []byte("staged-user-edit\n"), 0o644))
	gittest.Run(t, trunk, "add", "staged.txt")
	testutil.FailErr(t, "modify unstaged.txt", os.WriteFile(filepath.Join(trunk, "unstaged.txt"), []byte("unstaged-user-edit\n"), 0o644))

	return repoFixture{
		tmpDir:      tmp,
		trunkDir:    trunk,
		subSource:   subSrc,
		linkedWTDir: linkedWT,
	}
}

func newTestManager(t *testing.T, baseDir string) *Manager {
	t.Helper()
	mgrDir := filepath.Join(baseDir, "privategit-mgr")
	mgr, err := NewManager(ManagerConfig{BackingDir: mgrDir})
	testutil.FailErr(t, "NewManager", err)
	t.Cleanup(func() {
		_ = mgr.Store().Close()
	})
	return mgr
}

func newMountedViewWithManager(
	t *testing.T,
	mgr *Manager,
	viewID, trunkDir string,
	authorized ...string,
) *layeredfs.Core {
	t.Helper()
	backing := filepath.Join(mgr.cfg.BackingDir, "core-backing-"+viewID)
	testutil.FailErr(t, "mkdir core backing", os.MkdirAll(backing, 0o700))
	core, err := layeredfs.New(layeredfs.Config{
		ViewID:         viewID,
		BackingDir:     backing,
		GitInterceptor: mgr,
		Roots: []layeredfs.RootSpec{
			{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true},
		},
	})
	testutil.FailErr(t, "layeredfs.New", err)
	testutil.FailErr(t, "MountedClaim", mgr.MountedClaim(viewID, "worker-"+viewID, "primary", trunkDir, core, authorized...))
	t.Cleanup(func() {
		_ = mgr.ReleaseView(viewID)
		_ = core.ReleaseView()
	})
	return core
}

func snapshotDirectoryTreeDigests(t *testing.T, root string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rErr := filepath.Rel(root, path)
		if rErr != nil || rel == "." {
			return rErr
		}
		slash := filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 {
			link, lErr := os.Readlink(path)
			if lErr != nil {
				return lErr
			}
			out[slash] = "symlink:" + link
			return nil
		}
		if d.IsDir() {
			out[slash] = "dir"
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(raw)
		out[slash] = hex.EncodeToString(sum[:])
		return nil
	})
	testutil.FailErr(t, "snapshotDirectoryTreeDigests", err)
	return out
}

func assertDigestMapsEqual(t *testing.T, label string, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("%s: entry count changed: before=%d after=%d", label, len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			if strings.HasSuffix(k, ".git/index") {
				t.Fatalf("%s: trunk .git/index was mutated", label)
			}
			t.Fatalf("%s: path %q changed: before=%s after=%s", label, k, v, after[k])
		}
	}
}
