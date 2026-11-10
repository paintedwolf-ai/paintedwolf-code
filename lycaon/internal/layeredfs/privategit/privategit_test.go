package privategit

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestOperationSuiteMountedAndMaterialized(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"mounted", "materialized"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fix := buildRichRepoFixture(t)
			trunkBefore := snapshotDirectoryTreeDigests(t, fix.trunkDir)

			mgr := newTestManager(t, fix.tmpDir)
			var core *layeredfs.Core
			viewID := "view-" + mode

			if mode == "mounted" {
				core = newMountedViewWithManager(t, mgr, viewID, fix.trunkDir)
				// Mounted claim must not scan for .git or initialize repositories eagerly.
				met, ok := mgr.RepoMetrics(viewID, ".")
				if ok || met.State != StateUninitialized {
					t.Fatalf("expected uninitialized repo before first access, got %+v (ok=%v)", met, ok)
				}
				if stats := core.Stats(); stats.LowerDirReads != 0 {
					t.Fatalf("expected 0 lower dir reads on mounted claim, got %d", stats.LowerDirReads)
				}
			} else {
				targetDir := filepath.Join(fix.tmpDir, "mat-target")
				backing := filepath.Join(fix.tmpDir, "mat-core-backing")
				testutil.FailErr(t, "mkdir mat-target", os.MkdirAll(targetDir, 0o755))
				testutil.FailErr(t, "mkdir mat-core-backing", os.MkdirAll(backing, 0o700))
				var err error
				core, err = layeredfs.New(layeredfs.Config{
					ViewID:         viewID,
					BackingDir:     backing,
					GitInterceptor: mgr,
					Roots: []layeredfs.RootSpec{
						{ID: "primary", Label: "primary", TrunkPath: targetDir, IsPrimary: true},
					},
				})
				testutil.FailErr(t, "layeredfs.New materialized", err)
				t.Cleanup(func() {
					_ = mgr.ReleaseView(viewID)
					_ = core.ReleaseView()
				})
				seedRes, err := mgr.InitializeMaterializedSeed(t.Context(), MaterializedSeedConfig{
					ViewID:    viewID,
					WorkerID:  "worker-" + viewID,
					TrunkDir:  fix.trunkDir,
					TargetDir: targetDir,
					Core:      core,
				})
				testutil.FailErr(t, "InitializeMaterializedSeed", err)
				if seedRes.EntriesSeeded == 0 || len(seedRes.Repositories) < 2 {
					t.Fatalf("expected main repo and submodule seeded, got %+v", seedRes)
				}
			}

			exec := NewGitExecutor(mgr, viewID, RemotePolicy{})

			// 1. status & diff: verify staged and unstaged source changes are preserved.
			statusOut, err := exec.Status(t.Context(), "--porcelain")
			testutil.FailErr(t, "git status --porcelain", err)
			if !strings.Contains(statusOut, "M  staged.txt") || !strings.Contains(statusOut, " M unstaged.txt") {
				t.Fatalf("expected staged and unstaged distinction preserved, got:\n%s", statusOut)
			}
			unstagedDiff, err := exec.Diff(t.Context())
			testutil.FailErr(t, "git diff", err)
			if !strings.Contains(unstagedDiff, "+unstaged-user-edit") {
				t.Fatalf("expected unstaged diff, got:\n%s", unstagedDiff)
			}
			stagedDiff, err := exec.Diff(t.Context(), "--cached")
			testutil.FailErr(t, "git diff --cached", err)
			if !strings.Contains(stagedDiff, "+staged-user-edit") {
				t.Fatalf("expected staged diff, got:\n%s", stagedDiff)
			}

			// Verify initialization metrics and that alternates never point to trunk.
			met, ok := mgr.RepoMetrics(viewID, ".")
			if !ok || met.State != StateReady || met.ObjectsImported == 0 || met.BytesImported == 0 {
				t.Fatalf("expected ready InitMetrics with imported objects, got %+v", met)
			}
			altBytes, err := core.ReadFile(layeredfs.QualifiedPath{RootID: "primary", Rel: ".git/objects/info/alternates"})
			testutil.FailErr(t, "read .git/objects/info/alternates via Core", err)
			altStr := strings.TrimSpace(string(altBytes))
			if altStr != mgr.Store().ObjectsDir() || strings.Contains(altStr, fix.trunkDir) {
				t.Fatalf("alternates must point only to retained store %q, got %q", mgr.Store().ObjectsDir(), altStr)
			}

			// Prove index refresh (`git status`) did not manufacture authored file changes in Core!
			logicalChanges, err := core.EnumerateChanges(true)
			testutil.FailErr(t, "EnumerateChanges(true) after status", err)
			if len(logicalChanges) != 0 {
				t.Fatalf("index refresh must not manufacture authored file changes, got %+v", logicalChanges)
			}

			// 2. log & commit (commits the pre-staged change without touching unstaged.txt).
			logBefore, err := exec.Log(t.Context(), "--oneline", "-n", "3")
			testutil.FailErr(t, "git log", err)
			if !strings.Contains(logBefore, "add submodule sub") {
				t.Fatalf("unexpected log output:\n%s", logBefore)
			}
			_, err = exec.Commit(t.Context(), "worker commits staged file")
			testutil.FailErr(t, "git commit", err)

			// 3. stash push & pop (stashes unstaged.txt and restores it).
			_, err = exec.Stash(t.Context(), "push", "-m", "worker-stash")
			testutil.FailErr(t, "git stash push", err)
			_, err = exec.Stash(t.Context(), "pop")
			testutil.FailErr(t, "git stash pop", err)

			// 4. revert HEAD (reverts the worker commit and updates staged.txt in Core).
			_, err = exec.Run(t.Context(), ".", "revert", "--no-edit", "HEAD")
			testutil.FailErr(t, "git revert HEAD", err)
			revertedBytes, err := core.ReadFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "staged.txt"})
			testutil.FailErr(t, "read staged.txt after revert", err)
			if string(revertedBytes) != "staged-base\n" {
				t.Fatalf("expected reverted staged.txt content, got %q", string(revertedBytes))
			}

			// 5. switch & checkout across branches.
			_, err = exec.Run(t.Context(), ".", "switch", "-c", "worker-feature")
			testutil.FailErr(t, "git switch -c worker-feature", err)
			_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "feature.txt"}, []byte("feature-work\n"), 0o644)
			testutil.FailErr(t, "write feature.txt", err)
			_, err = exec.Run(t.Context(), ".", "add", "feature.txt")
			testutil.FailErr(t, "git add feature.txt", err)
			_, err = exec.Commit(t.Context(), "add feature.txt")
			testutil.FailErr(t, "git commit feature.txt", err)
			_, err = exec.Checkout(t.Context(), "main")
			testutil.FailErr(t, "git checkout main", err)
			_, err = exec.Run(t.Context(), ".", "switch", "worker-feature")
			testutil.FailErr(t, "git switch worker-feature", err)

			// 6. worktree list & submodule status + submodule git operations.
			wtList, err := exec.Run(t.Context(), ".", "worktree", "list")
			testutil.FailErr(t, "git worktree list", err)
			if strings.TrimSpace(wtList) == "" {
				t.Fatal("expected non-empty worktree list")
			}
			subStatus, err := exec.Run(t.Context(), ".", "submodule", "status")
			testutil.FailErr(t, "git submodule status", err)
			if !strings.Contains(subStatus, "sub") {
				t.Fatalf("expected sub in submodule status, got %q", subStatus)
			}
			subLog, err := exec.Run(t.Context(), "sub", "log", "--oneline", "-n", "1")
			testutil.FailErr(t, "submodule git log", err)
			if !strings.Contains(subLog, "sub initial commit") {
				t.Fatalf("unexpected submodule log: %q", subLog)
			}

			// 7. Linked worktree initialization & operation.
			wtCore := newMountedViewWithManager(t, mgr, viewID+"-linkedwt", fix.linkedWTDir, fix.trunkDir)
			wtExec := NewGitExecutor(mgr, viewID+"-linkedwt", RemotePolicy{})
			wtStatus, err := wtExec.Status(t.Context(), "--short", "--branch")
			testutil.FailErr(t, "linked worktree git status", err)
			if !strings.Contains(wtStatus, "wt-branch") {
				t.Fatalf("expected linked worktree on wt-branch, got %q", wtStatus)
			}
			wtDotGitAttr, err := wtCore.Stat(layeredfs.QualifiedPath{RootID: "primary", Rel: ".git"})
			testutil.FailErr(t, "stat linked worktree .git", err)
			if wtDotGitAttr.Kind != layeredfs.EntryDirectory {
				t.Fatalf("linked worktree .git must be virtualized as private directory, got %+v", wtDotGitAttr)
			}

			// 8. Prove trunk HEAD, refs, index, config, objects, and working tree are 100% unchanged!
			trunkAfter := snapshotDirectoryTreeDigests(t, fix.trunkDir)
			assertDigestMapsEqual(t, "trunk unchanged after worker operations ("+mode+")", trunkBefore, trunkAfter)
		})
	}
}

func TestSecurityConfinementGitlinksConfigAndNativeTools(t *testing.T) {
	t.Parallel()
	fix := buildRichRepoFixture(t)
	mgr := newTestManager(t, fix.tmpDir)

	// 1. Inject malicious config keys into trunk .git/config before worker initialization.
	cfgPath := filepath.Join(fix.trunkDir, ".git", "config")
	origCfg, err := os.ReadFile(cfgPath)
	testutil.FailErr(t, "read trunk config", err)
	maliciousCfg := string(origCfg) + `
[core]
	worktree = /tmp/hijack-worktree
	gitdir = /tmp/hijack-gitdir
	hooksPath = /tmp/hijack-hooks
	fsmonitor = /tmp/hijack-fsmonitor
	sshCommand = /tmp/hijack-ssh
[include]
	path = /tmp/hijack-include
[alias]
	st = !/tmp/hijack-alias
`
	testutil.FailErr(t, "write malicious trunk config", os.WriteFile(cfgPath, []byte(maliciousCfg), 0o644))

	core := newMountedViewWithManager(t, mgr, "view-sec", fix.trunkDir)
	qp := func(rel string) layeredfs.QualifiedPath {
		return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
	}

	privCfgBytes, err := core.ReadFile(qp(".git/config"))
	testutil.FailErr(t, "read private .git/config", err)
	privCfg := string(privCfgBytes)
	for _, forbidden := range []string{"hijack-worktree", "hijack-gitdir", "hijack-hooks", "hijack-fsmonitor", "hijack-include", "hijack-alias"} {
		if strings.Contains(privCfg, forbidden) {
			t.Fatalf("expected malicious config token %q to be stripped from private config:\n%s", forbidden, privCfg)
		}
	}

	// 2. Native file-tool mutation restriction on .git paths.
	if err := CheckNativeToolPath(".git/config", true); !errors.Is(err, ErrNativeToolGitMutationForbidden) {
		t.Fatalf("expected ErrNativeToolGitMutationForbidden from CheckNativeToolPath, got %v", err)
	}
	if err := CheckNativeToolPath("sub/.git/HEAD", true); !errors.Is(err, ErrNativeToolGitMutationForbidden) {
		t.Fatalf("expected ErrNativeToolGitMutationForbidden on submodule .git, got %v", err)
	}
	testutil.FailErr(t, "SetAccessOrigin OriginNativeTool", SetAccessOrigin(mgr, "view-sec", OriginNativeTool))
	if _, err := core.WriteFile(qp(".git/injected"), []byte("bad"), 0o644); !errors.Is(err, ErrNativeToolGitMutationForbidden) {
		t.Fatalf("expected ErrNativeToolGitMutationForbidden on Core write under OriginNativeTool, got %v", err)
	}
	testutil.FailErr(t, "restore OriginFilesystem", SetAccessOrigin(mgr, "view-sec", OriginFilesystem))

	// 3. Unauthorized external gitlink target and cross-worker gitlink redirection must be rejected.
	badRoot := filepath.Join(fix.tmpDir, "bad-gitlink-repo")
	testutil.FailErr(t, "mkdir bad-gitlink-repo", os.MkdirAll(badRoot, 0o755))
	outsideTarget := filepath.Join(fix.tmpDir, "unauthorized-outside-git")
	testutil.FailErr(t, "mkdir outsideTarget", os.MkdirAll(outsideTarget, 0o755))
	testutil.FailErr(t, "write unauthorized .git file", os.WriteFile(filepath.Join(badRoot, ".git"), []byte("gitdir: "+outsideTarget+"\n"), 0o644))

	badCore := newMountedViewWithManager(t, mgr, "view-bad-gitlink", badRoot)
	_, err = badCore.Stat(qp(".git"))
	if !errors.Is(err, ErrUnauthorizedGitTarget) || !errors.Is(err, layeredfs.ErrPathEscape) {
		t.Fatalf("expected ErrUnauthorizedGitTarget / ErrPathEscape for unauthorized gitlink, got %v", err)
	}

	// Pointing gitlink at another worker's private .git directory must also be refused.
	worker1GitDir, err := readyPrivateGitDir(mgr, "view-sec", ".")
	testutil.FailErr(t, "readyPrivateGitDir view-sec", err)
	testutil.FailErr(t, "write cross-worker .git file", os.WriteFile(filepath.Join(badRoot, ".git"), []byte("gitdir: "+worker1GitDir+"\n"), 0o644))
	if _, err := badCore.Stat(qp(".git")); !errors.Is(err, ErrUnauthorizedGitTarget) {
		t.Fatalf("expected ErrUnauthorizedGitTarget for cross-worker gitlink, got %v", err)
	}

	// 4. Materialized seed limits and context cancellation.
	_, err = mgr.InitializeMaterializedSeed(t.Context(), MaterializedSeedConfig{
		ViewID:    "view-limit-entries",
		TrunkDir:  fix.trunkDir,
		TargetDir: filepath.Join(fix.tmpDir, "limit-entries"),
		Limits:    SeedLimits{MaxEntries: 1},
	})
	if !errors.Is(err, ErrSeedLimitExceeded) {
		t.Fatalf("expected ErrSeedLimitExceeded for MaxEntries=1, got %v", err)
	}

	cancelCtx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = mgr.InitializeMaterializedSeed(cancelCtx, MaterializedSeedConfig{
		ViewID:    "view-canceled",
		TrunkDir:  fix.trunkDir,
		TargetDir: filepath.Join(fix.tmpDir, "canceled-target"),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on canceled seed, got %v", err)
	}
}

func TestSnapshotMetadataDigestsConcurrentAbsentAndPresentFiles(t *testing.T) {
	t.Parallel()
	repoDir := t.TempDir()
	gittest.Init(t, repoDir)
	testutil.FailErr(t, "write a.txt", os.WriteFile(filepath.Join(repoDir, "a.txt"), []byte("hello\n"), 0o644))
	gittest.CommitAll(t, repoDir, "initial")
	// Remove index and packed-refs so HEAD and refs/ are present while index and packed-refs are absent.
	_ = os.Remove(filepath.Join(repoDir, ".git", "index"))
	_ = os.Remove(filepath.Join(repoDir, ".git", "packed-refs"))

	layout, err := resolveSourceRepoLayout(repoDir, ".", nil, nil)
	testutil.FailErr(t, "resolveSourceRepoLayout", err)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				digests, hasLock, sErr := snapshotMetadataDigests(layout)
				if sErr != nil || hasLock {
					t.Errorf("snapshotMetadataDigests failed (hasLock=%v): %v", hasLock, sErr)
					return
				}
				if digests["index"] != "absent" || digests["packed-refs"] != "absent" || digests["HEAD"] == "" || digests["HEAD"] == "absent" {
					t.Errorf("unexpected digests: %+v", digests)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestUnifiedCaptureProductionPathMutationLockAndCleanup(t *testing.T) {
	t.Parallel()
	repoDir := t.TempDir()
	gittest.Init(t, repoDir)
	testutil.FailErr(t, "write f.txt", os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("v1\n"), 0o644))
	gittest.CommitAll(t, repoDir, "c1")

	layout, err := resolveSourceRepoLayout(repoDir, ".", nil, nil)
	testutil.FailErr(t, "resolveSourceRepoLayout", err)

	// 1. Direct tryCaptureSourceMetadataOnce: mutation during hook must return false and clean stageGitDir.
	stage1 := filepath.Join(t.TempDir(), "stage-mutate")
	testutil.FailErr(t, "mkdir stage1", os.MkdirAll(stage1, 0o755))
	origConfig, err := os.ReadFile(filepath.Join(repoDir, ".git", "config"))
	testutil.FailErr(t, "read config", err)
	coherent, err := tryCaptureSourceMetadataOnce(layout, stage1, 0, func(wt string, _ int) {
		_ = os.WriteFile(filepath.Join(wt, ".git", "config"), append(origConfig, []byte("\n# mutated\n")...), 0o644)
	})
	testutil.FailErr(t, "restore config", os.WriteFile(filepath.Join(repoDir, ".git", "config"), origConfig, 0o644))
	if err != nil || coherent {
		t.Fatalf("expected coherent=false, err=nil on config mutation during capture, got coherent=%v err=%v", coherent, err)
	}
	if _, statErr := os.Stat(stage1); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("expected failed capture stage1 to be cleaned up, stat err=%v", statErr)
	}

	// 2. Direct tryCaptureSourceMetadataOnce: lock file appearing during hook must return false and clean stageGitDir.
	stage2 := filepath.Join(t.TempDir(), "stage-lock")
	testutil.FailErr(t, "mkdir stage2", os.MkdirAll(stage2, 0o755))
	lockFile := filepath.Join(repoDir, ".git", "refs", "heads", "main.lock")
	coherent, err = tryCaptureSourceMetadataOnce(layout, stage2, 0, func(_ string, _ int) {
		_ = os.WriteFile(lockFile, []byte("lock"), 0o644)
	})
	_ = os.Remove(lockFile)
	if err != nil || coherent {
		t.Fatalf("expected coherent=false, err=nil on ref lock during capture, got coherent=%v err=%v", coherent, err)
	}
	if _, statErr := os.Stat(stage2); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("expected failed capture stage2 to be cleaned up, stat err=%v", statErr)
	}

	// 3. Manager capture retry and exhausted-retry staging cleanup.
	backing := filepath.Join(t.TempDir(), "mgr-backing")
	mgr, err := NewManager(ManagerConfig{
		BackingDir:        backing,
		MaxCaptureRetries: 2,
		CaptureAttemptHook: func(wt string, _ int) {
			_ = os.WriteFile(filepath.Join(wt, ".git", "index.lock"), []byte("locked"), 0o644)
		},
	})
	testutil.FailErr(t, "NewManager", err)
	t.Cleanup(func() { _ = mgr.Store().Close() })

	core := newMountedViewWithManager(t, mgr, "view-exhausted", repoDir)
	_, err = core.Stat(layeredfs.QualifiedPath{RootID: "primary", Rel: ".git/HEAD"})
	_ = os.Remove(filepath.Join(repoDir, ".git", "index.lock"))
	if !errors.Is(err, layeredfs.ErrGitPending) {
		t.Fatalf("expected ErrGitPending when capture retries exhausted by index.lock, got %v", err)
	}
	stageBase := filepath.Join(backing, "views", "view-exhausted", "git-staging")
	if entries, rErr := os.ReadDir(stageBase); rErr == nil && len(entries) != 0 {
		t.Fatalf("expected 0 leftover staging directories in %s, got %d", stageBase, len(entries))
	}
}

func TestPrivateGitFallbackDistinguishesNoSourceRepoFromEstablishedAndFailed(t *testing.T) {
	t.Parallel()

	t.Run("genuine_non_repository", func(t *testing.T) {
		t.Parallel()
		fix := buildRichRepoFixture(t)
		mgr := newTestManager(t, fix.tmpDir)
		core := newMountedViewWithManager(t, mgr, "view-nonrepo", fix.trunkDir)

		_, err := core.Mkdir(layeredfs.QualifiedPath{RootID: "primary", Rel: "nested"}, 0o755)
		testutil.FailErr(t, "mkdir nested", err)
		_, err = core.Mkdir(layeredfs.QualifiedPath{RootID: "primary", Rel: "nested/.git"}, 0o755)
		testutil.FailErr(t, "mkdir nested/.git", err)
		_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "nested/.git/HEAD"}, []byte("ref: refs/heads/main\n"), 0o644)
		testutil.FailErr(t, "write nested/.git/HEAD", err)

		rec := readDurableRepoRecord(mgr, "view-nonrepo", "nested")
		if rec.Status != repoStatusNoSourceRepo {
			t.Fatalf("expected durable status %q for nested, got %+v", repoStatusNoSourceRepo, rec)
		}
		if _, gErr := readyPrivateGitDir(mgr, "view-nonrepo", "nested"); !errors.Is(gErr, ErrNoSourceRepository) {
			t.Fatalf("expected ErrNoSourceRepository from readyPrivateGitDir, got %v", gErr)
		}
	})

	t.Run("established_private_history_with_source_git_removed_and_store_failure", func(t *testing.T) {
		t.Parallel()
		srcDir := t.TempDir()
		gittest.Init(t, srcDir)
		testutil.FailErr(t, "write p.txt", os.WriteFile(filepath.Join(srcDir, "p.txt"), []byte("committed\n"), 0o644))
		gittest.CommitAll(t, srcDir, "initial")

		mgr := newTestManager(t, t.TempDir())
		core := newMountedViewWithManager(t, mgr, "view-est", srcDir)
		qp := func(rel string) layeredfs.QualifiedPath {
			return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
		}

		// Establish private repository.
		_, err := core.ReadFile(qp(".git/HEAD"))
		testutil.FailErr(t, "initial ReadFile(.git/HEAD)", err)
		rec := readDurableRepoRecord(mgr, "view-est", ".")
		if rec.Status != repoStatusEstablished {
			t.Fatalf("expected durable status %q, got %+v", repoStatusEstablished, rec)
		}

		// Remove source .git after private repository is established.
		testutil.FailErr(t, "remove source .git", os.RemoveAll(filepath.Join(srcDir, ".git")))

		// Induce a private-store failure by removing the retained store objects directory.
		mgr.Store().mu.Lock()
		unlockStoreWriteBoundary(mgr.Store())
		mgr.Store().mu.Unlock()
		testutil.FailErr(t, "remove store objectsDir", os.RemoveAll(mgr.Store().ObjectsDir()))

		// Both read and write paths must fail closed with ErrGitPending, never falling back to mutable backing.
		if _, err := core.Stat(qp(".git/HEAD")); !errors.Is(err, layeredfs.ErrGitPending) {
			t.Fatalf("expected ErrGitPending on Stat(.git/HEAD) after store failure, got %v", err)
		}
		if _, err := core.WriteFile(qp(".git/config"), []byte("[core]\n"), 0o644); !errors.Is(err, layeredfs.ErrGitPending) {
			t.Fatalf("expected ErrGitPending on WriteFile(.git/config) after store failure, got %v", err)
		}
		if _, err := core.Mkdir(qp(".git/refs/heads/newdir"), 0o755); !errors.Is(err, layeredfs.ErrGitPending) {
			t.Fatalf("expected ErrGitPending on Mkdir inside .git after store failure, got %v", err)
		}
	})

	t.Run("failed_initialization_with_source_git_removed", func(t *testing.T) {
		t.Parallel()
		srcDir := t.TempDir()
		gittest.Init(t, srcDir)
		testutil.FailErr(t, "write p.txt", os.WriteFile(filepath.Join(srcDir, "p.txt"), []byte("committed\n"), 0o644))
		gittest.CommitAll(t, srcDir, "initial")

		mgr := newTestManager(t, t.TempDir())
		mgr.cfg.FailObjectImportHook = func(_, _ string) error {
			return errors.New("injected import failure")
		}
		core := newMountedViewWithManager(t, mgr, "view-failed-init", srcDir)
		qp := func(rel string) layeredfs.QualifiedPath {
			return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
		}

		if _, err := core.Stat(qp(".git/HEAD")); !errors.Is(err, layeredfs.ErrGitPending) {
			t.Fatalf("expected initial ErrGitPending, got %v", err)
		}
		// Now remove source .git and clear hook: prior failure must not downgrade to ErrNoSourceRepository.
		testutil.FailErr(t, "remove source .git", os.RemoveAll(filepath.Join(srcDir, ".git")))
		mgr.cfg.FailObjectImportHook = nil

		if _, err := core.Stat(qp(".git/HEAD")); !errors.Is(err, layeredfs.ErrGitPending) {
			t.Fatalf("expected ErrGitPending after source .git removed following failed init, got %v", err)
		}
		if _, err := core.Mkdir(qp(".git"), 0o755); !errors.Is(err, layeredfs.ErrGitPending) {
			t.Fatalf("expected ErrGitPending on Mkdir(.git) after failed init, got %v", err)
		}
	})
}
