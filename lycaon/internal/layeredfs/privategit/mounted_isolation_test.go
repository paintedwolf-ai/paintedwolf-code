package privategit

import (
	"errors"
	"fmt"
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

	// Prove clean trunk files share the underlying inode (0 bytes copied) AND live in-place
	// edits to previously clean trunk files are immediately visible to direct git status/diff
	// on arbitrary pathspecs inside wtDir without any wrapper or skip-worktree manipulation.
	tInfo, err := os.Stat(filepath.Join(trunk, "clean_042.txt"))
	testutil.FailErr(t, "stat trunk clean_042.txt", err)
	wInfo, err := os.Stat(filepath.Join(wtDir, "clean_042.txt"))
	testutil.FailErr(t, "stat wtDir clean_042.txt", err)
	if !os.SameFile(tInfo, wInfo) {
		t.Fatalf("expected clean_042.txt to share inode with trunk without copying bytes")
	}
	testutil.FailErr(t, "live edit trunk clean_042.txt", os.WriteFile(filepath.Join(trunk, "clean_042.txt"), []byte("live-trunk-mutation\n"), 0o644))
	directSt, code, err := gitexec.Run(t.Context(), wtDir, []string{"status", "--porcelain=v1"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if err != nil || code != 0 || !strings.Contains(string(directSt), " M clean_042.txt") {
		t.Fatalf("expected direct git status in wtDir to observe live clean_042.txt change, got (code %d, err %v):\n%s", code, err, string(directSt))
	}
	directDiff, code, err := gitexec.Run(t.Context(), wtDir, []string{"diff", "--", "clean_*.txt"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if err != nil || code != 0 || !strings.Contains(string(directDiff), "+live-trunk-mutation") {
		t.Fatalf("expected direct git diff with glob pathspec to observe live clean_042.txt change, got (code %d, err %v):\n%s", code, err, string(directDiff))
	}
	testutil.FailErr(t, "restore trunk clean_042.txt", os.WriteFile(filepath.Join(trunk, "clean_042.txt"), []byte("payload-42\n"), 0o644))

	// Worker upper-layer write unlinks the hardlink before writing so canonical trunk is never mutated.
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "clean_001.txt"}, []byte("worker-private-override\n"), 0o644)
	testutil.FailErr(t, "core.WriteFile clean_001.txt", err)
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "worker_edit.txt"}, []byte("worker-new\n"), 0o644)
	testutil.FailErr(t, "core.WriteFile worker_edit.txt", err)

	exec := NewGitExecutor(mgr, "view-sparse", RemotePolicy{})
	_, err = exec.Run(t.Context(), ".", "add", "clean_001.txt", "worker_edit.txt")
	testutil.FailErr(t, "git add", err)
	_, err = exec.Commit(t.Context(), "worker mounted commit")
	testutil.FailErr(t, "git commit", err)

	trunkAfter := snapshotDirectoryTreeDigests(t, trunk)
	assertDigestMapsEqual(t, "canonical trunk after mounted operations", trunkBefore, trunkAfter)
}

func TestTwoWorkersSharingTrunkIsolatedViewResolution(t *testing.T) {
	t.Parallel()
	fix := buildRichRepoFixture(t)
	mgr := newTestManager(t, fix.tmpDir)

	_ = newMountedViewWithManager(t, mgr, "view-alpha", fix.trunkDir)
	_ = newMountedViewWithManager(t, mgr, "view-beta", fix.trunkDir)

	mountAlias := filepath.Join(fix.tmpDir, "mounted-alpha")
	testutil.FailErr(t, "BindMountedWorktree", mgr.BindMountedWorktree("view-alpha", mountAlias))

	wtAlpha := mgr.ViewWorktreeDir("view-alpha")
	wtBeta := mgr.ViewWorktreeDir("view-beta")
	if wtAlpha != mountAlias || wtBeta == "" || wtAlpha == wtBeta {
		t.Fatalf("expected distinct private worktree dirs, got alpha=%q beta=%q", wtAlpha, wtBeta)
	}

	for i := 0; i < 30; i++ {
		gotA, finA, okA, errA := PrepareViewGitOperation(t.Context(), fix.trunkDir, "worker-view-alpha")
		testutil.FailErr(t, "PrepareViewGitOperation alpha", errA)
		_ = finA()
		gotM, finM, okM, errM := PrepareViewGitOperation(t.Context(), mountAlias, "worker-view-alpha")
		testutil.FailErr(t, "PrepareViewGitOperation mountAlias", errM)
		_ = finM()
		gotB, finB, okB, errB := PrepareViewGitOperation(t.Context(), fix.trunkDir, "worker-view-beta")
		testutil.FailErr(t, "PrepareViewGitOperation beta", errB)
		_ = finB()
		if !okA || gotA != wtAlpha || !okM || gotM != wtAlpha || !okB || gotB != wtBeta {
			t.Fatalf("iter %d: cross-selected views: gotA=%q gotM=%q (want %q), gotB=%q (want %q)", i, gotA, gotM, wtAlpha, gotB, wtBeta)
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

func TestInjectedCaptureAndObjectRetentionFailuresPreserveRecovery(t *testing.T) {
	t.Parallel()
	fix := buildRichRepoFixture(t)
	mgr := newTestManager(t, fix.tmpDir)

	// 1. Corrupt source .git/index before initial capture: initialization must fail with ErrCorruptObjectStore / ErrGitPending
	// without leaving a half-ready view, and succeed cleanly once the source index is restored.
	idxPath := filepath.Join(fix.trunkDir, ".git", "index")
	origIdx, err := os.ReadFile(idxPath)
	testutil.FailErr(t, "read trunk index", err)
	testutil.FailErr(t, "corrupt trunk index", os.WriteFile(idxPath, []byte("corrupt-index-bytes"), 0o644))

	core := newMountedViewWithManager(t, mgr, "view-fail-recover", fix.trunkDir)
	if _, _, _, pErr := PrepareViewGitOperation(t.Context(), fix.trunkDir, "worker-view-fail-recover"); !errors.Is(pErr, ErrCorruptObjectStore) {
		t.Fatalf("expected ErrCorruptObjectStore on corrupt source index capture, got %v", pErr)
	}
	if _, sErr := core.Stat(layeredfs.QualifiedPath{RootID: "primary", Rel: ".git/HEAD"}); !errors.Is(sErr, layeredfs.ErrGitPending) {
		t.Fatalf("expected ErrGitPending on Stat(.git/HEAD) while source index is corrupt, got %v", sErr)
	}

	testutil.FailErr(t, "restore trunk index", os.WriteFile(idxPath, origIdx, 0o644))
	wtDir, finish, managed, err := PrepareViewGitOperation(t.Context(), fix.trunkDir, "worker-view-fail-recover")
	testutil.FailErr(t, "PrepareViewGitOperation after index repair", err)
	if !managed || wtDir == "" {
		t.Fatalf("expected managed worktree after repair, got managed=%v wtDir=%q", managed, wtDir)
	}
	testutil.FailErr(t, "finish after repair", finish())

	// 2. Inject object-retention failure across finish(), Commit(), and CaptureCheckpointGitState():
	// each must surface ErrCorruptObjectStore while preserving worker working-tree and private-git state for recovery.
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "recover_me.txt"}, []byte("precious-worker-work\n"), 0o644)
	testutil.FailErr(t, "write recover_me.txt", err)

	injectedRetainErr := errors.New("injected retention failure")
	mgr.cfg.FailObjectRetentionHook = func(vID, _ string) error {
		if vID == "view-fail-recover" {
			return injectedRetainErr
		}
		return nil
	}

	_, finFail, _, err := PrepareViewGitOperation(t.Context(), fix.trunkDir, "worker-view-fail-recover")
	testutil.FailErr(t, "PrepareViewGitOperation before finish failure", err)
	if fErr := finFail(); !errors.Is(fErr, ErrCorruptObjectStore) {
		t.Fatalf("expected finish() to propagate ErrCorruptObjectStore on retention failure, got %v", fErr)
	}

	exec := NewGitExecutor(mgr, "view-fail-recover", RemotePolicy{})
	if _, addErr := exec.Run(t.Context(), ".", "add", "recover_me.txt"); !errors.Is(addErr, ErrCorruptObjectStore) {
		t.Fatalf("expected exec.Run(add) to propagate ErrCorruptObjectStore on retention failure, got %v", addErr)
	}
	if _, comErr := exec.Commit(t.Context(), "commit survives retention failure"); !errors.Is(comErr, ErrCorruptObjectStore) {
		t.Fatalf("expected exec.Commit to propagate ErrCorruptObjectStore on retention failure, got %v", comErr)
	}
	if _, cpErr := mgr.CaptureCheckpointGitState(t.Context(), "view-fail-recover", "cp-fail"); !errors.Is(cpErr, ErrCorruptObjectStore) {
		t.Fatalf("expected CaptureCheckpointGitState to propagate ErrCorruptObjectStore on retention failure, got %v", cpErr)
	}

	// Clear the injected hook and prove the worker's file and private commit were preserved and can now be checkpointed.
	mgr.cfg.FailObjectRetentionHook = nil
	gotBytes, err := core.ReadFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "recover_me.txt"})
	testutil.FailErr(t, "ReadFile recover_me.txt after retention failure", err)
	if string(gotBytes) != "precious-worker-work\n" {
		t.Fatalf("expected worker file preserved for recovery, got %q", string(gotBytes))
	}
	cp, err := mgr.CaptureCheckpointGitState(t.Context(), "view-fail-recover", "cp-recovered")
	testutil.FailErr(t, "CaptureCheckpointGitState after clearing hook", err)
	testutil.FailErr(t, "VerifyCheckpointHistory after recovery", mgr.VerifyCheckpointHistory(t.Context(), cp))
}
