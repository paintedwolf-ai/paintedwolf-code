package privategit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

func populateDisposableTestMount(t *testing.T, trunkDir, mountDir string) {
	t.Helper()
	testutil.FailErr(t, "mkdir mountDir", os.MkdirAll(mountDir, 0o755))
	err := filepath.WalkDir(trunkDir, func(path string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return wErr
		}
		rel, rErr := filepath.Rel(trunkDir, path)
		if rErr != nil || rel == "." {
			return rErr
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		dst := filepath.Join(mountDir, rel)
		info, iErr := d.Info()
		if iErr != nil {
			return iErr
		}
		if d.Type()&fs.ModeSymlink != 0 {
			link, lErr := os.Readlink(path)
			if lErr != nil {
				return lErr
			}
			_ = os.RemoveAll(dst)
			return os.Symlink(link, dst)
		}
		if d.IsDir() {
			return os.MkdirAll(dst, info.Mode().Perm()|0o700)
		}
		return copyWorkingTreeFile(path, dst, info.Mode())
	})
	testutil.FailErr(t, "populateDisposableTestMount", err)
}

func newMountedViewWithManager(
	t *testing.T,
	mgr *Manager,
	viewID, trunkDir string,
	authorized ...string,
) *layeredfs.Core {
	t.Helper()
	backing := filepath.Join(mgr.cfg.BackingDir, "core-backing-"+viewID)
	mountDir := filepath.Join(mgr.cfg.BackingDir, "test-mount-"+viewID)
	testutil.FailErr(t, "mkdir core backing", os.MkdirAll(backing, 0o700))
	populateDisposableTestMount(t, trunkDir, mountDir)
	core, err := layeredfs.New(layeredfs.Config{
		ViewID:         viewID,
		BackingDir:     backing,
		GitInterceptor: mgr,
		Roots: []layeredfs.RootSpec{
			{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true},
		},
	})
	testutil.FailErr(t, "layeredfs.New", err)
	testutil.FailErr(t, "MountedClaimWithWorktree", mgr.MountedClaimWithWorktree(viewID, "worker-"+viewID, "primary", trunkDir, mountDir, core, authorized...))
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
		info, iErr := d.Info()
		if iErr != nil {
			return iErr
		}
		if d.IsDir() {
			out[slash] = fmt.Sprintf("dir:%04o", info.Mode().Perm())
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(raw)
		out[slash] = fmt.Sprintf("mode=%04o:sha=%s", info.Mode().Perm(), hex.EncodeToString(sum[:]))
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

func TestCrashRecoveryRefImportIdempotencyConflictAndDeletedRefSafety(t *testing.T) {
	t.Parallel()
	fix := buildRichRepoFixture(t)
	mgr := newTestManager(t, fix.tmpDir)
	core := newMountedViewWithManager(t, mgr, "view-import", fix.trunkDir)
	exec := NewGitExecutor(mgr, "view-import", RemotePolicy{})

	_, err := exec.Run(t.Context(), ".", "branch", "-D", "side-branch")
	testutil.FailErr(t, "delete side-branch privately", err)
	_, err = exec.Switch(t.Context(), "-c", "worker-br")
	testutil.FailErr(t, "switch -c worker-br", err)
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "imported.txt"}, []byte("import-me\n"), 0o644)
	testutil.FailErr(t, "write imported.txt", err)
	_, _ = exec.Run(t.Context(), ".", "add", "imported.txt")
	_, err = exec.Commit(t.Context(), "worker branch commit")
	testutil.FailErr(t, "commit worker-br", err)
	_, err = exec.Run(t.Context(), ".", "tag", "v1-worker")
	testutil.FailErr(t, "tag v1-worker", err)
	_, err = exec.Stash(t.Context(), "push", "-m", "checkpointed-stash")
	testutil.FailErr(t, "stash push", err)

	cp, err := mgr.CaptureCheckpointGitState(t.Context(), "view-import", "cp-crash-1")
	testutil.FailErr(t, "CaptureCheckpointGitState", err)

	allOpts := RefImportOptionFlags{IncludeDetachedHead: true, IncludeTags: true, IncludeStashes: true}
	for _, crashAt := range []ImportStage{StageObjectsImported, StageMidRefPublish, StageRefsPublished} {
		simErr := errors.New("simulated crash at " + string(crashAt))
		_, err := mgr.ImportRefsToTrunk(t.Context(), RefImportRequest{
			JobID: "job-crash", WorkerID: "worker-view-import", CheckpointID: "cp-crash-1",
			TrunkDir: fix.trunkDir, Checkpoint: cp, Options: allOpts,
			CrashHook: func(stage ImportStage) error {
				if stage == crashAt {
					return simErr
				}
				return nil
			},
		})
		if !errors.Is(err, simErr) {
			t.Fatalf("expected simulated crash at %s, got %v", crashAt, err)
		}
	}

	res, err := mgr.ImportRefsToTrunk(t.Context(), RefImportRequest{
		JobID: "job-crash", WorkerID: "worker-view-import", CheckpointID: "cp-crash-1",
		TrunkDir: fix.trunkDir, Checkpoint: cp, Options: allOpts,
	})
	testutil.FailErr(t, "idempotent ImportRefsToTrunk retry", err)

	for _, wantRef := range []string{
		"refs/paintedwolf/workers/job-crash/cp-crash-1/heads/worker-br",
		"refs/paintedwolf/workers/job-crash/cp-crash-1/tags/v1-worker",
		"refs/paintedwolf/workers/job-crash/cp-crash-1/stash/0",
	} {
		if res.ImportedRefs[wantRef] == "" {
			t.Fatalf("missing expected imported ref %s in %+v", wantRef, res.ImportedRefs)
		}
	}

	if !strings.Contains(gittest.Run(t, fix.trunkDir, "branch", "--list", "side-branch"), "side-branch") {
		t.Fatalf("private branch deletion must never delete trunk branch")
	}
	if _, err := os.Stat(filepath.Join(fix.trunkDir, "imported.txt")); !os.IsNotExist(err) {
		t.Fatalf("ref import must not apply files to trunk working tree, got err=%v", err)
	}

	if _, err := mgr.ImportRefsToTrunk(t.Context(), RefImportRequest{
		JobID: "job-crash", WorkerID: "wrong-worker-id", CheckpointID: "cp-crash-1",
		TrunkDir: fix.trunkDir, Checkpoint: cp,
	}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("expected ErrIdentityMismatch, got %v", err)
	}

	mainOID := strings.TrimSpace(gittest.Run(t, fix.trunkDir, "rev-parse", "refs/heads/main"))
	gittest.Run(t, fix.trunkDir, "update-ref", "refs/paintedwolf/workers/job-conflict/cp-crash-1/heads/worker-br", mainOID)
	if _, err := mgr.ImportRefsToTrunk(t.Context(), RefImportRequest{
		JobID: "job-conflict", WorkerID: "worker-view-import", CheckpointID: "cp-crash-1",
		TrunkDir: fix.trunkDir, Checkpoint: cp,
	}); !errors.Is(err, ErrRefImportConflict) {
		t.Fatalf("expected ErrRefImportConflict when destination ref has different OID, got %v", err)
	}
}
