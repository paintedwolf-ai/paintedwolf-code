package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestEagerCopyGitStoreReuseConfigSanitizationAndUnauthorizedGitlinks(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcRoot := createFixtureRepoWithSubmodule(t, filepath.Join(tmp, "source"))
	rawCfg := "[core]\n\tfsmonitor = true\n\thooksPath = /tmp/evil\n[remote \"origin\"]\n\turl = https://user:secret@example.com/org/repo.git\n"
	if err := os.WriteFile(filepath.Join(srcRoot, ".git", "config"), []byte(rawCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	adp := NewEagerCopyAdapter(ReasonMountDenied)
	view1, err := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "git-reuse-1",
		RuntimeDir: filepath.Join(tmp, "rt"),
		Grants:     []RootGrant{{RootID: "repo", Label: "repo", SourcePath: srcRoot, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare view1 failed: %v", err)
	}
	rootRepo1, ok := view1.SeededGitRepos["repo:."]
	if !ok || rootRepo1.ObjectCount == 0 {
		t.Fatalf("expected initial object import on view1, got %+v", view1.SeededGitRepos)
	}
	cfgBytes, err := os.ReadFile(filepath.Join(rootRepo1.PrivateMetaDir, "config"))
	if err != nil {
		t.Fatalf("read sanitized config: %v", err)
	}
	if strings.Contains(string(cfgBytes), "fsmonitor") || strings.Contains(string(cfgBytes), "hooksPath") || !strings.Contains(string(cfgBytes), "logallrefupdates") {
		t.Fatalf("expected sanitized git config, got:\n%s", string(cfgBytes))
	}
	altBytes, err := os.ReadFile(filepath.Join(rootRepo1.PrivateMetaDir, "objects", "info", "alternates"))
	if err != nil || strings.TrimSpace(string(altBytes)) == "" {
		t.Fatalf("expected rewritten alternates pointing to retained object store, err=%v content=%q", err, string(altBytes))
	}

	view2, err := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "git-reuse-2",
		RuntimeDir: filepath.Join(tmp, "rt"),
		Grants:     []RootGrant{{RootID: "repo", Label: "repo", SourcePath: srcRoot, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare view2 failed: %v", err)
	}
	rootRepo2, ok := view2.SeededGitRepos["repo:."]
	if !ok || rootRepo2.ObjectsReused == 0 || rootRepo2.BytesReused == 0 {
		t.Fatalf("expected retained object store reuse on view2, got %+v", view2.SeededGitRepos)
	}
	_, _ = adp.Release(ctx, view1.ViewID)
	_, _ = adp.Release(ctx, view2.ViewID)

	verifyGitlinkRejectionsAndLimits(t, ctx, tmp)
}

func verifyGitlinkRejectionsAndLimits(t *testing.T, ctx context.Context, tmp string) {
	t.Helper()
	adp := NewEagerCopyAdapter(ReasonMountDenied)

	// 1. External gitlink outside granted roots must be rejected.
	outsideGit := filepath.Join(tmp, "outside-git")
	if err := os.MkdirAll(outsideGit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideGit, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	extRepo := filepath.Join(tmp, "ext-repo")
	if err := os.MkdirAll(extRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extRepo, ".git"), []byte("gitdir: "+outsideGit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "unauth-gitlink",
		RuntimeDir: filepath.Join(tmp, "rt"),
		Grants:     []RootGrant{{RootID: "repo", Label: "repo", SourcePath: extRepo, IsPrimary: true}},
	})
	if err == nil || (!errors.Is(err, privategit.ErrUnauthorizedGitTarget) && !strings.Contains(err.Error(), string(ReasonUnauthorizedRoot))) {
		t.Fatalf("expected unauthorized git target rejection, got %v", err)
	}

	// 2. Unresolved gitlink must fail without publishing ready repository.
	badLinkRepo := filepath.Join(tmp, "badlink-repo")
	if err := os.MkdirAll(badLinkRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badLinkRepo, ".git"), []byte("gitdir: ./.git/modules/missing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = adp.Prepare(ctx, PrepareRequest{
		ViewID:     "unresolved-gitlink",
		RuntimeDir: filepath.Join(tmp, "rt"),
		Grants:     []RootGrant{{RootID: "repo", Label: "repo", SourcePath: badLinkRepo, IsPrimary: true}},
	})
	if err == nil {
		t.Fatalf("expected unresolved gitlink to fail Prepare")
	}

	// 3. Git seeding counts against the copy budget, and passing it cleans up like any other overrun.
	bigGitRepo := createFixtureRepoWithSubmodule(t, filepath.Join(tmp, "biggit-repo"))
	_, err = adp.Prepare(ctx, PrepareRequest{
		ViewID:     "git-byte-limit",
		RuntimeDir: filepath.Join(tmp, "rt"),
		Grants:     []RootGrant{{RootID: "repo", Label: "repo", SourcePath: bigGitRepo, IsPrimary: true}},
		CopyBudget: &CopyBudget{Limit: 12, EntryCost: 1},
	})
	var overrun *BudgetOverrunError
	if !errors.As(err, &overrun) {
		t.Fatalf("expected a budget overrun on the git byte limit, got %v", err)
	}
}

func TestEagerCopyJournalSyncDirectoryRemovalSymlinkReplacementAndMetadata(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	srcRoot := filepath.Join(tmp, "sync-src")
	if err := os.MkdirAll(filepath.Join(srcRoot, "olddir", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcRoot, "dir2file"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "olddir", "nested", "gone.txt"), []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "dir2file", "child.txt"), []byte("child"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "file2dir"), []byte("was-file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "modeonly.sh"), []byte("#!/bin/sh\necho hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("modeonly.sh", filepath.Join(srcRoot, "link")); err != nil {
		t.Fatal(err)
	}

	adp := NewEagerCopyAdapter(ReasonMountDenied)
	view, err := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "sync-journal-1",
		RuntimeDir: filepath.Join(tmp, "rt"),
		Grants:     []RootGrant{{RootID: "repo", Label: "repo", SourcePath: srcRoot, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	applyMaterializedFilesystemMutations(t, view.PrimaryWorkPath)

	receipt, releaseBarrier, err := adp.CheckpointBarrier(ctx, view.ViewID)
	if err != nil || !receipt.DirtyDataFlushed {
		t.Fatalf("CheckpointBarrier failed: receipt=%+v err=%v", receipt, err)
	}
	verifyJournaledMutationsInCore(t, view)
	_ = releaseBarrier()

	// Verify sync errors are propagated rather than ignored when Core view is released early.
	_ = view.Core.ReleaseView()
	if err := os.WriteFile(filepath.Join(view.PrimaryWorkPath, "after-release.txt"), []byte("fail"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adp.CheckpointBarrier(ctx, view.ViewID); err == nil {
		t.Fatalf("expected CheckpointBarrier to propagate Core sync error after view release")
	}
	_, _ = adp.Release(ctx, view.ViewID)
}

func applyMaterializedFilesystemMutations(t *testing.T, workPath string) {
	t.Helper()
	// 1. Delete nested directory tree.
	if err := os.RemoveAll(filepath.Join(workPath, "olddir")); err != nil {
		t.Fatal(err)
	}
	// 2. Replace directory with regular file.
	if err := os.RemoveAll(filepath.Join(workPath, "dir2file")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workPath, "dir2file"), []byte("now-a-file"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 3. Replace regular file with directory + child file.
	if err := os.Remove(filepath.Join(workPath, "file2dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workPath, "file2dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workPath, "file2dir", "inside.txt"), []byte("inside-dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 4. Retarget symlink.
	if err := os.Remove(filepath.Join(workPath, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("dir2file", filepath.Join(workPath, "link")); err != nil {
		t.Fatal(err)
	}
	// 5. Metadata-only chmod on unchanged file content.
	if err := os.Chmod(filepath.Join(workPath, "modeonly.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func verifyJournaledMutationsInCore(t *testing.T, view *PreparedView) {
	t.Helper()
	changes, err := view.Core.EnumerateChanges(true)
	if err != nil || len(changes) == 0 {
		t.Fatalf("expected enumerated changes in Core, err=%v len=%d", err, len(changes))
	}
	if _, err := view.Core.Lstat(layeredfs.QualifiedPath{RootID: "repo", Rel: "olddir"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected deleted directory olddir to be unlinked in Core, got err=%v", err)
	}
	raw, err := view.Core.ReadFile(layeredfs.QualifiedPath{RootID: "repo", Rel: "dir2file"})
	if err != nil || string(raw) != "now-a-file" {
		t.Fatalf("expected dir2file replaced with file in Core, err=%v content=%q", err, string(raw))
	}
	insideRaw, err := view.Core.ReadFile(layeredfs.QualifiedPath{RootID: "repo", Rel: "file2dir/inside.txt"})
	if err != nil || string(insideRaw) != "inside-dir" {
		t.Fatalf("expected file2dir replaced with directory in Core, err=%v content=%q", err, string(insideRaw))
	}
	target, err := view.Core.Readlink(layeredfs.QualifiedPath{RootID: "repo", Rel: "link"})
	if err != nil || target != "dir2file" {
		t.Fatalf("expected retargeted symlink in Core, err=%v target=%q", err, target)
	}
	attr, err := view.Core.Lstat(layeredfs.QualifiedPath{RootID: "repo", Rel: "modeonly.sh"})
	if err != nil || attr.Mode.Perm()&0o111 == 0 {
		t.Fatalf("expected chmod-only change synced to Core, err=%v mode=%v", err, attr.Mode)
	}
}

func TestWorkspaceSelectionNarrowsMaterializedRootsWhileTaskPathHintsDoNot(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	rootA := filepath.Join(tmp, "rootA")
	rootB := filepath.Join(tmp, "rootB")
	_ = os.MkdirAll(filepath.Join(rootA, "pkg", "alpha"), 0o755)
	_ = os.MkdirAll(filepath.Join(rootA, "pkg", "beta"), 0o755)
	_ = os.MkdirAll(filepath.Join(rootB, "docs"), 0o755)
	_ = os.WriteFile(filepath.Join(rootA, "pkg", "alpha", "service.go"), []byte("package alpha\n"), 0o644)
	_ = os.WriteFile(filepath.Join(rootA, "pkg", "beta", "excluded.go"), []byte("package beta\n"), 0o644)
	_ = os.WriteFile(filepath.Join(rootB, "docs", "readme.md"), []byte("# docs\n"), 0o644)

	grants := []RootGrant{
		{RootID: "rootA", Label: "A", SourcePath: rootA, IsPrimary: true},
		{RootID: "rootB", Label: "B", SourcePath: rootB},
	}
	adp := NewEagerCopyAdapter(ReasonNone)

	view, err := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "narrowed-view",
		RuntimeDir: filepath.Join(tmp, "runtime"),
		Grants:     grants,
		Selection: WorkspaceSelection{
			RootIDs:  []string{"rootA"},
			Subroots: map[string]string{"rootA": "pkg/alpha"},
		},
		TaskPathHints: []string{"rootB/docs/readme.md", "rootA/pkg/beta/excluded.go"},
	})
	if err != nil {
		t.Fatalf("Prepare narrowed failed: %v", err)
	}
	if len(view.Roots) != 1 || view.Roots[0].RootID != "rootA" || view.Roots[0].Subroot != "pkg/alpha" {
		t.Fatalf("expected single narrowed rootA:pkg/alpha, got %+v", view.Roots)
	}
	if _, err := view.Core.ReadFile(layeredfs.QualifiedPath{RootID: "rootA", Rel: "service.go"}); err != nil {
		t.Fatalf("expected service.go in narrowed subroot, err=%v", err)
	}
	if _, err := view.Core.ReadFile(layeredfs.QualifiedPath{RootID: "rootA", Rel: "excluded.go"}); !os.IsNotExist(err) {
		t.Fatalf("excluded sibling subroot must not be materialized: %v", err)
	}

	_, unauthErr := adp.Prepare(ctx, PrepareRequest{
		ViewID:     "unauth-view",
		RuntimeDir: filepath.Join(tmp, "runtime"),
		Grants:     grants,
		Selection:  WorkspaceSelection{RootIDs: []string{"ungranted"}},
	})
	var aErr *AdapterError
	if !errors.As(unauthErr, &aErr) || aErr.ReasonCode != ReasonUnauthorizedRoot {
		t.Fatalf("expected ReasonUnauthorizedRoot, got %v", unauthErr)
	}
}

func TestLinkedWorktreeGitlinkIsAuthorizedOnlyByItsStructuralLinkage(t *testing.T) {
	gitDirFor := func(t *testing.T) (repo, worktree, admin string) {
		t.Helper()
		base, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		repo, worktree = filepath.Join(base, "repo"), filepath.Join(base, "wt")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gittest.InitCommit(t, repo, "init")
		gittest.Run(t, repo, "worktree", "add", "-b", "linked", worktree)
		return repo, worktree, filepath.Join(repo, ".git", "worktrees", "wt")
	}
	prepare := func(t *testing.T, viewID, source string) error {
		t.Helper()
		view, err := NewEagerCopyAdapter(ReasonMountDenied).Prepare(t.Context(), PrepareRequest{
			ViewID: viewID, RuntimeDir: t.TempDir(),
			Grants: []RootGrant{{RootID: "wt", Label: "wt", SourcePath: source, IsPrimary: true}},
		})
		if err == nil && view == nil {
			t.Fatal("Prepare returned neither view nor error")
		}
		return err
	}
	refused := func(t *testing.T, err error) {
		t.Helper()
		var adapterErr *AdapterError
		if !errors.As(err, &adapterErr) || adapterErr.ReasonCode != ReasonUnauthorizedRoot {
			t.Fatalf("want %s refusal, got %v", ReasonUnauthorizedRoot, err)
		}
	}

	t.Run("a real linked worktree prepares", func(t *testing.T) {
		_, worktree, _ := gitDirFor(t)
		if err := prepare(t, "linked-worktree", worktree); err != nil {
			t.Fatalf("linked worktree refused: %v", err)
		}
	})
	t.Run("a back-reference to another worktree is refused", func(t *testing.T) {
		_, worktree, admin := gitDirFor(t)
		if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(t.TempDir(), ".git")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		refused(t, prepare(t, "forged-back-reference", worktree))
	})
	t.Run("a gitlink borrowing another worktree's administrative directory is refused", func(t *testing.T) {
		_, _, admin := gitDirFor(t)
		impostor := t.TempDir()
		if err := os.WriteFile(filepath.Join(impostor, ".git"), []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		refused(t, prepare(t, "borrowed-administrative-dir", impostor))
	})
	t.Run("a common directory that does not hold the worktree is refused", func(t *testing.T) {
		_, worktree, admin := gitDirFor(t)
		elsewhere := t.TempDir()
		gittest.Init(t, elsewhere)
		if err := os.WriteFile(filepath.Join(admin, "commondir"), []byte(filepath.Join(elsewhere, ".git")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		refused(t, prepare(t, "foreign-common-dir", worktree))
	})
	t.Run("an administrative directory without a common directory is refused", func(t *testing.T) {
		_, worktree, admin := gitDirFor(t)
		if err := os.Remove(filepath.Join(admin, "commondir")); err != nil {
			t.Fatal(err)
		}
		refused(t, prepare(t, "no-common-dir", worktree))
	})
}
