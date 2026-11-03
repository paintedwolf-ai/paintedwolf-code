package privategit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
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
			_, err = exec.Revert(t.Context(), "HEAD")
			testutil.FailErr(t, "git revert HEAD", err)
			revertedBytes, err := core.ReadFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "staged.txt"})
			testutil.FailErr(t, "read staged.txt after revert", err)
			if string(revertedBytes) != "staged-base\n" {
				t.Fatalf("expected reverted staged.txt content, got %q", string(revertedBytes))
			}

			// 5. switch & checkout across branches.
			_, err = exec.Switch(t.Context(), "-c", "worker-feature")
			testutil.FailErr(t, "git switch -c worker-feature", err)
			_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "feature.txt"}, []byte("feature-work\n"), 0o644)
			testutil.FailErr(t, "write feature.txt", err)
			_, err = exec.Run(t.Context(), ".", "add", "feature.txt")
			testutil.FailErr(t, "git add feature.txt", err)
			_, err = exec.Commit(t.Context(), "add feature.txt")
			testutil.FailErr(t, "git commit feature.txt", err)
			_, err = exec.Checkout(t.Context(), "main")
			testutil.FailErr(t, "git checkout main", err)
			_, err = exec.Switch(t.Context(), "worker-feature")
			testutil.FailErr(t, "git switch worker-feature", err)

			// 6. worktree list & submodule status + submodule git operations.
			wtList, err := exec.WorktreeList(t.Context())
			testutil.FailErr(t, "git worktree list", err)
			if strings.TrimSpace(wtList) == "" {
				t.Fatal("expected non-empty worktree list")
			}
			subStatus, err := exec.SubmoduleStatus(t.Context())
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
	testutil.FailErr(t, "SetAccessOrigin OriginNativeTool", mgr.SetAccessOrigin("view-sec", OriginNativeTool))
	if _, err := core.WriteFile(qp(".git/injected"), []byte("bad"), 0o644); !errors.Is(err, ErrNativeToolGitMutationForbidden) {
		t.Fatalf("expected ErrNativeToolGitMutationForbidden on Core write under OriginNativeTool, got %v", err)
	}
	testutil.FailErr(t, "restore OriginFilesystem", mgr.SetAccessOrigin("view-sec", OriginFilesystem))

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
	worker1GitDir, err := mgr.PrivateGitDir("view-sec", ".")
	testutil.FailErr(t, "PrivateGitDir view-sec", err)
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
