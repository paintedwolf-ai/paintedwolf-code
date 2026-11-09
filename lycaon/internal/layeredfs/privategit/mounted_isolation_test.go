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

func TestPrepareViewGitOperationMountedBindingAndCopiedWorktreeDoubleIsolation(t *testing.T) {
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

	// (a) Verify absent and mismatched MountedViewBindings fail closed with ErrMountedWorktreeUnavailable.
	verifyAbsentAndMismatchedMountBindings(t, mgr, tmp, trunk)

	// (b) Verify a legitimately empty active mounted view (where all visible entries were deleted) succeeds,
	// and deactivating or revoking the mount binding fails closed with ErrMountedWorktreeUnavailable.
	verifyLegitimatelyEmptyActiveMountedView(t, mgr, tmp, trunk)

	// (c) Verify private Git metadata initialization and copied-worktree unit-test double / materialized seed isolation.
	// Note: newMountedViewWithManager uses populateCopiedWorktreeTestDouble (a unit-test double, not a kernel mount).
	core := newMountedViewWithManager(t, mgr, "view-mounted", trunk)
	wtDir, finish, managed, err := PrepareViewGitOperation(t.Context(), trunk, "worker-view-mounted")
	testutil.FailErr(t, "PrepareViewGitOperation", err)
	if !managed || wtDir == "" || wtDir == trunk {
		t.Fatalf("expected managed mounted worktree, got managed=%v wtDir=%q", managed, wtDir)
	}
	stOut, code, err := gitexec.Run(t.Context(), wtDir, []string{"status", "--porcelain=v1"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if err != nil || code != 0 || !strings.Contains(string(stOut), "M  staged.txt") || !strings.Contains(string(stOut), " M unstaged.txt") {
		t.Fatalf("expected staged and unstaged status preserved, got (code %d, err %v):\n%s", code, err, string(stOut))
	}
	testutil.FailErr(t, "finish prepare", finish())

	assertInPlaceWriteAndChmodIsolated(t, trunk, wtDir, "clean_042.txt")
	directSt, code, err := gitexec.Run(t.Context(), wtDir, []string{"status", "--porcelain=v1"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if err != nil || code != 0 || !strings.Contains(string(directSt), " M clean_042.txt") {
		t.Fatalf("expected direct git status in wtDir to observe clean_042.txt edit, got (code %d, err %v):\n%s", code, err, string(directSt))
	}
	directDiff, code, err := gitexec.Run(t.Context(), wtDir, []string{"diff", "--", "clean_*.txt"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if err != nil || code != 0 || !strings.Contains(string(directDiff), "+in-place-worker-overwrite") {
		t.Fatalf("expected direct git diff with glob pathspec to observe clean_042.txt edit, got (code %d, err %v):\n%s", code, err, string(directDiff))
	}

	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "worker_edit.txt"}, []byte("worker-new\n"), 0o644)
	testutil.FailErr(t, "core.WriteFile worker_edit.txt", err)
	exec := NewGitExecutor(mgr, "view-mounted", RemotePolicy{})
	_, err = exec.Run(t.Context(), ".", "add", "worker_edit.txt")
	testutil.FailErr(t, "git add", err)
	_, err = exec.Commit(t.Context(), "worker mounted commit")
	testutil.FailErr(t, "git commit", err)

	// (d) Verify materialized mode (eagercopy) shares zero trunk inodes and protects trunk from in-place write/truncate/chmod.
	matDir := filepath.Join(tmp, "materialized-wt")
	_, err = mgr.InitializeMaterializedSeed(t.Context(), MaterializedSeedConfig{
		ViewID: "view-mat", WorkerID: "worker-view-mat", TrunkDir: trunk, TargetDir: matDir,
	})
	testutil.FailErr(t, "InitializeMaterializedSeed", err)
	assertInPlaceWriteAndChmodIsolated(t, trunk, matDir, "clean_042.txt")

	trunkAfter := snapshotDirectoryTreeDigests(t, trunk)
	assertDigestMapsEqual(t, "canonical trunk bytes and modes after copied-double and materialized in-place mutations", trunkBefore, trunkAfter)
}

func verifyAbsentAndMismatchedMountBindings(t *testing.T, mgr *Manager, tmp, trunk string) {
	t.Helper()
	unboundCore, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-unbound", BackingDir: filepath.Join(tmp, "core-unbound"), GitInterceptor: mgr,
		Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}},
	})
	testutil.FailErr(t, "layeredfs.New unbound", err)
	defer func() { _ = unboundCore.ReleaseView() }()

	otherCore, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-other", BackingDir: filepath.Join(tmp, "core-other"), GitInterceptor: mgr,
		Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}},
	})
	testutil.FailErr(t, "layeredfs.New other", err)
	defer func() { _ = otherCore.ReleaseView() }()

	candDir := filepath.Join(tmp, "candidate-mount")
	testutil.FailErr(t, "mkdir candDir", os.MkdirAll(candDir, 0o755))
	otherTrunk := filepath.Join(tmp, "other-trunk")
	testutil.FailErr(t, "mkdir otherTrunk", os.MkdirAll(otherTrunk, 0o755))

	testutil.FailErr(t, "MountedClaim unbound", mgr.MountedClaim("view-unbound", "worker-unbound", "primary", trunk, unboundCore))
	defer func() { _ = mgr.ReleaseView("view-unbound") }()

	if _, _, _, uErr := PrepareViewGitOperation(t.Context(), trunk, "worker-unbound"); !errors.Is(uErr, ErrMountedWorktreeUnavailable) {
		t.Fatalf("expected ErrMountedWorktreeUnavailable on unbound MountedClaim, got %v", uErr)
	}
	if bErr := mgr.BindMountedWorktree("view-unbound", candDir); !errors.Is(bErr, ErrMountedWorktreeUnavailable) {
		t.Fatalf("expected ErrMountedWorktreeUnavailable when no MountedViewBinding is registered, got %v", bErr)
	}
	if cErr := mgr.MountedClaimWithWorktree("view-no-bind", "worker-no-bind", "primary", trunk, candDir, otherCore); !errors.Is(cErr, ErrMountedWorktreeUnavailable) {
		t.Fatalf("expected ErrMountedWorktreeUnavailable on MountedClaimWithWorktree without binding, got %v", cErr)
	}

	validBase := MountedViewBinding{
		ViewID: "view-unbound", WorkerID: "worker-unbound", RootID: "primary",
		TrunkDir: trunk, MountPath: candDir, AdapterID: "fuselinux",
		Core: unboundCore, MountID: "mnt-1", Active: true,
	}
	badBindings := []MountedViewBinding{
		{ViewID: "wrong-view", WorkerID: "worker-unbound", RootID: "primary", TrunkDir: trunk, MountPath: candDir, AdapterID: "fuselinux", Core: unboundCore, MountID: "mnt-1", Active: true},
		{ViewID: "view-unbound", WorkerID: "wrong-worker", RootID: "primary", TrunkDir: trunk, MountPath: candDir, AdapterID: "fuselinux", Core: unboundCore, MountID: "mnt-1", Active: true},
		{ViewID: "view-unbound", WorkerID: "worker-unbound", RootID: "wrong-root", TrunkDir: trunk, MountPath: candDir, AdapterID: "fuselinux", Core: unboundCore, MountID: "mnt-1", Active: true},
		{ViewID: "view-unbound", WorkerID: "worker-unbound", RootID: "primary", TrunkDir: trunk, MountPath: candDir, AdapterID: "fuselinux", Core: otherCore, MountID: "mnt-1", Active: true},
		{ViewID: "view-unbound", WorkerID: "worker-unbound", RootID: "primary", TrunkDir: otherTrunk, MountPath: candDir, AdapterID: "fuselinux", Core: unboundCore, MountID: "mnt-1", Active: true},
		{ViewID: "view-unbound", WorkerID: "worker-unbound", RootID: "primary", TrunkDir: trunk, MountPath: trunk, AdapterID: "fuselinux", Core: unboundCore, MountID: "mnt-1", Active: true},
		{ViewID: "view-unbound", WorkerID: "worker-unbound", RootID: "primary", TrunkDir: trunk, MountPath: filepath.Join(tmp, "missing-dir"), AdapterID: "fuselinux", Core: unboundCore, MountID: "mnt-1", Active: true},
		{ViewID: "view-unbound", WorkerID: "worker-unbound", RootID: "primary", TrunkDir: trunk, MountPath: candDir, AdapterID: "eagercopy", Core: unboundCore, MountID: "mnt-1", Active: true},
		{ViewID: "view-unbound", WorkerID: "worker-unbound", RootID: "primary", TrunkDir: trunk, MountPath: candDir, AdapterID: "materialized", Core: unboundCore, MountID: "mnt-1", Active: true},
		{ViewID: "view-unbound", WorkerID: "worker-unbound", RootID: "primary", TrunkDir: trunk, MountPath: candDir, AdapterID: "", Core: unboundCore, MountID: "mnt-1", Active: true},
		{ViewID: "view-unbound", WorkerID: "worker-unbound", RootID: "primary", TrunkDir: trunk, MountPath: candDir, AdapterID: "fuselinux", Core: unboundCore, MountID: "", Active: true},
		{ViewID: "view-unbound", WorkerID: "worker-unbound", RootID: "primary", TrunkDir: trunk, MountPath: candDir, AdapterID: "fuselinux", Core: unboundCore, MountID: "mnt-1", Active: false},
		{ViewID: "view-unbound", WorkerID: "worker-unbound", RootID: "primary", TrunkDir: trunk, MountPath: candDir, AdapterID: "fuselinux", Core: unboundCore, MountID: "mnt-1", Active: true, IsActive: func() bool { return false }},
	}
	_ = validBase
	for i, bad := range badBindings {
		if err := mgr.BindMountedView(bad); !errors.Is(err, ErrMountedWorktreeUnavailable) {
			t.Fatalf("case %d: expected ErrMountedWorktreeUnavailable from BindMountedView(%+v), got %v", i, bad, err)
		}
		if err := mgr.RegisterMountedViewBinding(bad); !errors.Is(err, ErrMountedWorktreeUnavailable) {
			t.Fatalf("case %d: expected ErrMountedWorktreeUnavailable from RegisterMountedViewBinding(%+v), got %v", i, bad, err)
		}
	}
}

func verifyLegitimatelyEmptyActiveMountedView(t *testing.T, mgr *Manager, tmp, trunk string) {
	t.Helper()
	emptyActiveDir := filepath.Join(tmp, "empty-active-mount")
	testutil.FailErr(t, "mkdir emptyActiveDir", os.MkdirAll(emptyActiveDir, 0o755))
	emptyCore, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-empty-active", BackingDir: filepath.Join(tmp, "core-empty-active"), GitInterceptor: mgr,
		Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}},
	})
	testutil.FailErr(t, "layeredfs.New empty-active", err)
	defer func() {
		_ = mgr.ReleaseView("view-empty-active")
		_ = emptyCore.ReleaseView()
	}()

	isLive := true
	testutil.FailErr(t, "MountedClaimWithBinding empty active", mgr.MountedClaimWithBinding(MountedViewBinding{
		ViewID: "view-empty-active", WorkerID: "worker-empty-active", RootID: "primary",
		TrunkDir: trunk, MountPath: emptyActiveDir, AdapterID: "nfsdarwin",
		Core: emptyCore, MountID: "mnt-empty-active", Active: true,
		IsActive: func() bool { return isLive },
	}))

	wtDir, finish, managed, err := PrepareViewGitOperation(t.Context(), trunk, "worker-empty-active")
	testutil.FailErr(t, "PrepareViewGitOperation on legitimately empty active mounted view", err)
	if !managed || wtDir != emptyActiveDir {
		t.Fatalf("expected empty active mounted view to bind %q, got managed=%v wtDir=%q", emptyActiveDir, managed, wtDir)
	}
	stOut, code, err := gitexec.Run(t.Context(), wtDir, []string{"status", "--porcelain=v1"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if err != nil || code != 0 || !strings.Contains(string(stOut), " D clean_000.txt") {
		t.Fatalf("expected git status in legitimately empty active mounted view to report deleted files, got (code %d, err %v):\n%s", code, err, string(stOut))
	}
	testutil.FailErr(t, "finish empty active view", finish())

	isLive = false
	if _, _, _, dErr := PrepareViewGitOperation(t.Context(), trunk, "worker-empty-active"); !errors.Is(dErr, ErrMountedWorktreeUnavailable) {
		t.Fatalf("expected ErrMountedWorktreeUnavailable when IsActive() becomes false, got %v", dErr)
	}
	isLive = true
	mgr.RevokeMountedViewBinding("view-empty-active", "primary")
	if _, _, _, rErr := PrepareViewGitOperation(t.Context(), trunk, "worker-empty-active"); !errors.Is(rErr, ErrMountedWorktreeUnavailable) {
		t.Fatalf("expected ErrMountedWorktreeUnavailable after RevokeMountedViewBinding, got %v", rErr)
	}
}

func assertInPlaceWriteAndChmodIsolated(t *testing.T, trunkDir, worktreeDir, rel string) {
	t.Helper()
	tPath := filepath.Join(trunkDir, rel)
	wPath := filepath.Join(worktreeDir, rel)
	tInfoBefore, err := os.Stat(tPath)
	testutil.FailErr(t, "stat trunk "+rel, err)
	wInfoBefore, err := os.Stat(wPath)
	testutil.FailErr(t, "stat worktree "+rel, err)
	if os.SameFile(tInfoBefore, wInfoBefore) {
		t.Fatalf("isolation violation: worktree %s shares raw trunk inode with %s", wPath, tPath)
	}
	f, err := os.OpenFile(wPath, os.O_WRONLY|os.O_TRUNC, 0)
	testutil.FailErr(t, "open in-place "+wPath, err)
	_, wErr := f.WriteString("in-place-worker-overwrite\n")
	testutil.FailErr(t, "write in-place "+wPath, errors.Join(wErr, f.Close()))
	testutil.FailErr(t, "chmod in-place "+wPath, os.Chmod(wPath, 0o600))

	tBytesAfter, err := os.ReadFile(tPath)
	testutil.FailErr(t, "read trunk "+rel, err)
	tInfoAfter, err := os.Stat(tPath)
	testutil.FailErr(t, "stat trunk after "+rel, err)
	if string(tBytesAfter) != "payload-42\n" || tInfoAfter.Mode().Perm() != tInfoBefore.Mode().Perm() {
		t.Fatalf("trunk file %s was mutated by worker in-place write/chmod: bytes=%q mode=%04o (want mode %04o)",
			tPath, string(tBytesAfter), tInfoAfter.Mode().Perm(), tInfoBefore.Mode().Perm())
	}
}

func TestTwoWorkersSharingTrunkIsolatedViewResolution(t *testing.T) {
	t.Parallel()
	fix := buildRichRepoFixture(t)
	mgr := newTestManager(t, fix.tmpDir)

	coreAlpha := newMountedViewWithManager(t, mgr, "view-alpha", fix.trunkDir)
	_ = newMountedViewWithManager(t, mgr, "view-beta", fix.trunkDir)

	mountAlias := filepath.Join(fix.tmpDir, "mounted-alpha")
	populateCopiedWorktreeTestDouble(t, fix.trunkDir, mountAlias)
	testutil.FailErr(t, "RegisterMountedViewBinding", mgr.RegisterMountedViewBinding(MountedViewBinding{
		ViewID: "view-alpha", WorkerID: "worker-view-alpha", RootID: "primary",
		TrunkDir: fix.trunkDir, MountPath: mountAlias, AdapterID: "fuselinux",
		Core: coreAlpha, MountID: "unit-mount-alpha-rebind", Active: true,
	}))
	testutil.FailErr(t, "BindMountedWorktree", mgr.BindMountedWorktree("view-alpha", mountAlias))

	wtAlpha := viewWorktreeDir(mgr, "view-alpha")
	wtBeta := viewWorktreeDir(mgr, "view-beta")
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
	if _, _, _, err := PrepareManagerViewOperation(t.Context(), mgr, wtAlpha, "worker-view-beta"); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("expected ErrIdentityMismatch for a manager view paired with another worker, got %v", err)
	}
}

func TestCoreGitOverlaySyncPrimaryAndSecondaryRootsToCheckpointAndExport(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunkPrimary := filepath.Join(tmp, "trunk-primary")
	trunkSecond := filepath.Join(tmp, "trunk-second")
	for _, dir := range []string{trunkPrimary, trunkSecond} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o755))
		gittest.Init(t, dir)
		gittest.Run(t, dir, "checkout", "-b", "main")
		testutil.FailErr(t, "seed "+dir, os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644))
		gittest.CommitAll(t, dir, "seed commit")
	}

	// Build a second commit in scratch clones so we can inject its loose objects + ref updates
	// through Core's upper layer (.git/objects/... and .git/refs/heads/...) as a mounted shell git commit does.
	scratchPrimary := filepath.Join(tmp, "scratch-primary")
	scratchSecond := filepath.Join(tmp, "scratch-second")
	gittest.Run(t, tmp, "clone", trunkPrimary, scratchPrimary)
	gittest.Run(t, tmp, "clone", trunkSecond, scratchSecond)
	testutil.FailErr(t, "write scratchPrimary", os.WriteFile(filepath.Join(scratchPrimary, "shell.txt"), []byte("primary-shell\n"), 0o644))
	gittest.CommitAll(t, scratchPrimary, "primary shell commit")
	primaryShellOID := strings.TrimSpace(gittest.Run(t, scratchPrimary, "rev-parse", "HEAD"))

	testutil.FailErr(t, "write scratchSecond", os.WriteFile(filepath.Join(scratchSecond, "shell2.txt"), []byte("second-shell\n"), 0o644))
	gittest.CommitAll(t, scratchSecond, "second shell commit")
	secondShellOID := strings.TrimSpace(gittest.Run(t, scratchSecond, "rev-parse", "HEAD"))

	mgr := newTestManager(t, tmp)
	const viewID = "view-multi-shell"
	core, err := layeredfs.New(layeredfs.Config{
		ViewID:         viewID,
		BackingDir:     filepath.Join(tmp, "core-backing"),
		GitInterceptor: mgr,
		Roots: []layeredfs.RootSpec{
			{ID: "primary", Label: "primary", TrunkPath: trunkPrimary, IsPrimary: true},
			{ID: "second", Label: "second", TrunkPath: trunkSecond, IsPrimary: false},
		},
	})
	testutil.FailErr(t, "layeredfs.New", err)
	defer func() {
		_ = mgr.ReleaseView(viewID)
		_ = core.ReleaseView()
	}()

	wtPrimary := filepath.Join(tmp, "wt-primary")
	wtSecond := filepath.Join(tmp, "wt-second")
	populateCopiedWorktreeTestDouble(t, trunkPrimary, wtPrimary)
	populateCopiedWorktreeTestDouble(t, trunkSecond, wtSecond)

	testutil.FailErr(t, "MountedClaimWithBinding primary", mgr.MountedClaimWithBinding(MountedViewBinding{
		ViewID: viewID, WorkerID: "worker-multi", RootID: "primary",
		TrunkDir: trunkPrimary, MountPath: wtPrimary, AdapterID: "nfsdarwin",
		Core: core, MountID: "mnt-primary", Active: true,
	}))
	testutil.FailErr(t, "BindSecondaryRoot second", mgr.BindSecondaryRoot(MountedViewBinding{
		ViewID: viewID, WorkerID: "worker-multi", RootID: "second",
		TrunkDir: trunkSecond, MountPath: wtSecond, AdapterID: "nfsdarwin",
		Core: core, MountID: "mnt-second", Active: true,
	}))

	// Force kernelMounted=true on both views so privateGitDir is isolated under BackingDir/views/.../private-git
	// while shell .git writes land in Core's private upper layer.
	for _, vid := range []string{viewID, SecondaryViewID(viewID, "second")} {
		v, gErr := getManagedView(mgr, vid)
		testutil.FailErr(t, "getManagedView "+vid, gErr)
		v.kernelMounted = true
		_, iErr := mgr.InitRepo(t.Context(), vid, ".")
		testutil.FailErr(t, "InitRepo "+vid, iErr)
	}

	writeLooseObjectsToCore := func(rootID, scratchDir string) {
		objRoot := filepath.Join(scratchDir, ".git", "objects")
		err := filepath.WalkDir(objRoot, func(p string, d os.DirEntry, wErr error) error {
			if wErr != nil || d.IsDir() {
				return wErr
			}
			rel, rErr := filepath.Rel(scratchDir, p)
			if rErr != nil {
				return rErr
			}
			slashRel := filepath.ToSlash(rel)
			if strings.HasPrefix(slashRel, ".git/objects/pack/") || strings.HasPrefix(slashRel, ".git/objects/info/") {
				return nil
			}
			raw, readErr := os.ReadFile(p)
			if readErr != nil {
				return readErr
			}
			_, writeErr := core.WriteFile(layeredfs.QualifiedPath{RootID: rootID, Rel: slashRel}, raw, 0o444)
			return writeErr
		})
		testutil.FailErr(t, "writeLooseObjectsToCore "+rootID, err)
	}

	writeLooseObjectsToCore("primary", scratchPrimary)
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: ".git/refs/heads/shell-primary"}, []byte(primaryShellOID+"\n"), 0o644)
	testutil.FailErr(t, "write primary ref via Core", err)
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: ".git/HEAD"}, []byte("ref: refs/heads/shell-primary\n"), 0o644)
	testutil.FailErr(t, "write primary HEAD via Core", err)

	writeLooseObjectsToCore("second", scratchSecond)
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "second", Rel: ".git/refs/heads/shell-second"}, []byte(secondShellOID+"\n"), 0o644)
	testutil.FailErr(t, "write second ref via Core", err)
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "second", Rel: ".git/HEAD"}, []byte("ref: refs/heads/shell-second\n"), 0o644)
	testutil.FailErr(t, "write second HEAD via Core", err)

	cpState, err := mgr.CaptureCheckpointGitState(t.Context(), viewID, "cp-shell-multi")
	testutil.FailErr(t, "CaptureCheckpointGitState", err)
	if cpState.HeadRef != "refs/heads/shell-primary" || cpState.HeadOID != primaryShellOID {
		t.Fatalf("primary HeadRef=%q HeadOID=%q, want refs/heads/shell-primary @ %s", cpState.HeadRef, cpState.HeadOID, primaryShellOID)
	}
	if cpState.Refs["refs/heads/shell-primary"] != primaryShellOID {
		t.Fatalf("primary refs missing refs/heads/shell-primary: %+v", cpState.Refs)
	}
	secState, ok := cpState.Roots["second"]
	if !ok || secState == nil {
		t.Fatalf("expected secondary root state in CheckpointGitState.Roots, got %+v", cpState.Roots)
	}
	if secState.HeadRef != "refs/heads/shell-second" || secState.HeadOID != secondShellOID {
		t.Fatalf("second HeadRef=%q HeadOID=%q, want refs/heads/shell-second @ %s", secState.HeadRef, secState.HeadOID, secondShellOID)
	}

	exportPrimary := filepath.Join(tmp, "export-primary")
	testutil.FailErr(t, "ExportCheckpointObjects primary", mgr.ExportCheckpointObjects(t.Context(), viewID, cpState, exportPrimary))
	exportSecond := filepath.Join(tmp, "export-second")
	testutil.FailErr(t, "ExportCheckpointObjects second", mgr.ExportCheckpointObjects(t.Context(), SecondaryViewID(viewID, "second"), secState, exportSecond))
}
