package privategit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestPrepareViewGitOperationDoesNotCopyTrunkOrWalkUnchangedFiles(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
	gittest.Init(t, trunk)
	gittest.Run(t, trunk, "checkout", "-b", "main")

	const cleanFileCount = 200
	for i := 0; i < cleanFileCount; i++ {
		name := fmt.Sprintf("clean_%03d.txt", i)
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(trunk, name), []byte(fmt.Sprintf("payload-%d\n", i)), 0o644))
	}
	testutil.FailErr(t, "write staged.txt", os.WriteFile(filepath.Join(trunk, "staged.txt"), []byte("staged-v1\n"), 0o644))
	testutil.FailErr(t, "write unstaged.txt", os.WriteFile(filepath.Join(trunk, "unstaged.txt"), []byte("unstaged-v1\n"), 0o644))
	gittest.CommitAll(t, trunk, "initial 200-file commit")

	testutil.FailErr(t, "edit staged.txt", os.WriteFile(filepath.Join(trunk, "staged.txt"), []byte("staged-v2\n"), 0o644))
	gittest.Run(t, trunk, "add", "staged.txt")
	testutil.FailErr(t, "edit unstaged.txt", os.WriteFile(filepath.Join(trunk, "unstaged.txt"), []byte("unstaged-v2\n"), 0o644))

	trunkBefore := snapshotDirectoryTreeDigests(t, trunk)

	mgr := newTestManager(t, tmp)
	core := newMountedViewWithManager(t, mgr, "view-sparse", trunk)

	wtDir, finish, managed, err := PrepareViewGitOperation(t.Context(), trunk, "worker-view-sparse")
	testutil.FailErr(t, "PrepareViewGitOperation", err)
	if !managed || wtDir == "" || wtDir == trunk {
		t.Fatalf("expected managed private worktree stage, got managed=%v wtDir=%q", managed, wtDir)
	}
	stOut, code, err := gitexec.Run(t.Context(), wtDir, []string{"status", "--porcelain=v1"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if err != nil || code != 0 {
		t.Fatalf("status failed (code %d): %v: %s", code, err, string(stOut))
	}
	statusStr := string(stOut)
	if !strings.Contains(statusStr, "M  staged.txt") || !strings.Contains(statusStr, " M unstaged.txt") {
		t.Fatalf("expected staged and unstaged status preserved without clean file noise, got:\n%s", statusStr)
	}
	testutil.FailErr(t, "finish prepare", finish())

	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "worker_edit.txt"}, []byte("worker-new\n"), 0o644)
	testutil.FailErr(t, "core.WriteFile worker_edit.txt", err)

	exec := NewGitExecutor(mgr, "view-sparse", RemotePolicy{})
	_, err = exec.Run(t.Context(), ".", "add", "worker_edit.txt")
	testutil.FailErr(t, "git add worker_edit.txt", err)
	_, err = exec.Commit(t.Context(), "worker sparse commit")
	testutil.FailErr(t, "git commit", err)

	var materializedFiles []string
	_ = filepath.WalkDir(wtDir, func(path string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return nil
		}
		rel, rErr := filepath.Rel(wtDir, path)
		if rErr != nil || rel == "." {
			return nil
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			materializedFiles = append(materializedFiles, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(materializedFiles) > 4 {
		t.Fatalf("expected sparse worktree stage to contain only dirty/modified files (<=4), got %d: %v", len(materializedFiles), materializedFiles)
	}
	for _, f := range materializedFiles {
		if strings.HasPrefix(f, "clean_") {
			t.Fatalf("untouched trunk file %q must not be copied into mounted worktree stage", f)
		}
	}

	trunkAfter := snapshotDirectoryTreeDigests(t, trunk)
	assertDigestMapsEqual(t, "canonical trunk after sparse mounted operations", trunkBefore, trunkAfter)
}

func TestTwoWorkersSharingTrunkIsolatedViewResolution(t *testing.T) {
	t.Parallel()
	fix := buildRichRepoFixture(t)
	mgr := newTestManager(t, fix.tmpDir)

	_ = newMountedViewWithManager(t, mgr, "view-alpha", fix.trunkDir)
	_ = newMountedViewWithManager(t, mgr, "view-beta", fix.trunkDir)

	wtAlpha := mgr.ViewWorktreeDir("view-alpha")
	wtBeta := mgr.ViewWorktreeDir("view-beta")
	if wtAlpha == "" || wtBeta == "" || wtAlpha == wtBeta {
		t.Fatalf("expected distinct private worktree dirs, got alpha=%q beta=%q", wtAlpha, wtBeta)
	}

	for i := 0; i < 50; i++ {
		gotA, finA, okA, errA := PrepareViewGitOperation(t.Context(), fix.trunkDir, "worker-view-alpha")
		testutil.FailErr(t, "PrepareViewGitOperation alpha", errA)
		_ = finA()
		gotB, finB, okB, errB := PrepareViewGitOperation(t.Context(), fix.trunkDir, "worker-view-beta")
		testutil.FailErr(t, "PrepareViewGitOperation beta", errB)
		_ = finB()
		if !okA || gotA != wtAlpha || !okB || gotB != wtBeta {
			t.Fatalf("iter %d: cross-selected views: gotA=%q (want %q), gotB=%q (want %q)", i, gotA, wtAlpha, gotB, wtBeta)
		}
	}

	if _, _, _, err := PrepareViewGitOperation(t.Context(), wtAlpha, "worker-view-beta"); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("expected ErrIdentityMismatch when worker-view-beta accesses alpha worktree, got %v", err)
	}
	if _, _, _, err := PrepareViewGitOperation(t.Context(), wtBeta, "worker-view-alpha"); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("expected ErrIdentityMismatch when worker-view-alpha accesses beta worktree, got %v", err)
	}
	if _, _, _, err := PrepareViewGitOperation(t.Context(), wtAlpha, ""); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("expected ErrIdentityMismatch when private worktree is accessed without workerID, got %v", err)
	}
	if _, _, _, err := PrepareViewGitOperation(t.Context(), filepath.Join(fix.tmpDir, "unrelated"), "worker-view-alpha"); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("expected ErrIdentityMismatch when workerID is paired with unrelated root, got %v", err)
	}
	if _, ok := LookupExecutor(mgr, wtAlpha, "worker-view-beta"); ok {
		t.Fatalf("LookupExecutor must reject mismatched workerID and worktreeDir")
	}
}

func TestRetainedObjectPoolSurvivesSourcePruningAndFsck(t *testing.T) {
	t.Parallel()
	fix := buildRichRepoFixture(t)

	gittest.Run(t, fix.trunkDir, "checkout", "-b", "prune-me")
	testutil.FailErr(t, "write prune_me.txt", os.WriteFile(filepath.Join(fix.trunkDir, "prune_me.txt"), []byte("retained-in-pool\n"), 0o644))
	gittest.CommitAll(t, fix.trunkDir, "base commit to be pruned in source")
	prunedBaseOID := strings.TrimSpace(gittest.Run(t, fix.trunkDir, "rev-parse", "HEAD"))
	gittest.Run(t, fix.trunkDir, "checkout", "main")

	mgr := newTestManager(t, fix.tmpDir)
	core := newMountedViewWithManager(t, mgr, "view-pool", fix.trunkDir)
	exec := NewGitExecutor(mgr, "view-pool", RemotePolicy{})

	_, err := exec.Checkout(t.Context(), "prune-me")
	testutil.FailErr(t, "worker checkout prune-me", err)

	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "worker_pool.txt"}, []byte("worker-created-object\n"), 0o644)
	testutil.FailErr(t, "write worker_pool.txt", err)
	_, err = exec.Run(t.Context(), ".", "add", "worker_pool.txt")
	testutil.FailErr(t, "git add worker_pool.txt", err)
	_, err = exec.Commit(t.Context(), "worker commit retained in shared pool")
	testutil.FailErr(t, "worker commit", err)

	wtDir := mgr.ViewWorktreeDir("view-pool")
	workerHeadOut, _, err := gitexec.Run(t.Context(), wtDir, []string{"rev-parse", "HEAD"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	testutil.FailErr(t, "rev-parse worker HEAD", err)
	workerHeadOID := strings.TrimSpace(string(workerHeadOut))

	gittest.Run(t, fix.trunkDir, "branch", "-D", "prune-me")
	gittest.Run(t, fix.trunkDir, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
	gittest.Run(t, fix.trunkDir, "gc", "--prune=now")

	if _, code, _ := gitexec.Run(t.Context(), fix.trunkDir, []string{"cat-file", "-e", prunedBaseOID}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); code == 0 {
		t.Fatalf("expected %s to be pruned from canonical trunk", prunedBaseOID)
	}

	_, err = exec.Run(t.Context(), ".", "fsck", "--full")
	testutil.FailErr(t, "worker git fsck --full after source prune", err)
	showOut, err := exec.Run(t.Context(), ".", "show", "--stat", workerHeadOID)
	testutil.FailErr(t, "worker git show after source prune", err)
	if !strings.Contains(showOut, "worker_pool.txt") {
		t.Fatalf("expected worker commit content in git show, got:\n%s", showOut)
	}

	for _, oid := range []string{prunedBaseOID, workerHeadOID} {
		if _, code, cErr := gitexec.Run(
			t.Context(),
			mgr.Store().RootDir(),
			[]string{"--git-dir=" + mgr.Store().RootDir(), "cat-file", "-e", oid},
			gitexec.Opts{Profile: gitexec.ProfileHermetic},
		); cErr != nil || code != 0 {
			t.Fatalf("expected retained object store to directly contain %s (code %d): %v", oid, code, cErr)
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
