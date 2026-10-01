package gitrepo_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/testutil"
)

// plainRepo writes the .git directory layout an ordinary clone has, without running git.
func plainRepo(t *testing.T) string {
	t.Helper()
	root := canonicalTempDir(t)
	testutil.FailErr(t, "mkdir .git", os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	testutil.FailErr(t, "write config", os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n\tbare = false\n"), 0o644))
	return root
}

// canonicalTempDir returns a temp dir already in the spelling Discover reports, so a
// comparison is about discovery rather than about /var vs /private/var on darwin.
func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir := gitrepo.CanonicalDir(t.TempDir())
	if dir == "" {
		t.Fatal("temp dir did not canonicalize")
	}
	return dir
}

func TestDiscoverFindsRootFromSubdirectory(t *testing.T) {
	root := plainRepo(t)
	sub := filepath.Join(root, "packages", "web", "src")
	testutil.FailErr(t, "mkdir sub", os.MkdirAll(sub, 0o755))

	repo, ok := gitrepo.Discover(sub)
	if !ok {
		t.Fatalf("Discover(%q) found no repository", sub)
	}
	if repo.Root != root {
		t.Fatalf("Root = %q, want %q", repo.Root, root)
	}
	if want := filepath.Join(root, ".git"); repo.GitDir != want {
		t.Fatalf("GitDir = %q, want %q", repo.GitDir, want)
	}
	if repo.CommonDir != repo.GitDir {
		t.Fatalf("CommonDir = %q, want it to equal GitDir %q", repo.CommonDir, repo.GitDir)
	}
}

func TestDiscoverReportsOneIdentityForEveryDirectoryInTheRepo(t *testing.T) {
	root := plainRepo(t)
	web := filepath.Join(root, "packages", "web")
	testutil.FailErr(t, "mkdir web", os.MkdirAll(web, 0o755))

	top, ok := gitrepo.Discover(root)
	if !ok {
		t.Fatal("Discover(root) found no repository")
	}
	sub, ok := gitrepo.Discover(web)
	if !ok {
		t.Fatal("Discover(web) found no repository")
	}
	if top != sub {
		t.Fatalf("two roots in one checkout disagreed: %+v vs %+v", top, sub)
	}
}

func TestDiscoverOutsideAnyRepository(t *testing.T) {
	dir := canonicalTempDir(t)
	if repo, ok := gitrepo.Discover(dir); ok {
		t.Fatalf("Discover on a plain directory returned %+v", repo)
	}
	if repo, ok := gitrepo.Discover(""); ok {
		t.Fatalf("Discover(\"\") returned %+v", repo)
	}
}

func TestDiscoverLinkedWorktreeResolvesGitDirAndCommonDir(t *testing.T) {
	main := plainRepo(t)
	commonDir := filepath.Join(main, ".git")
	gitDir := filepath.Join(commonDir, "worktrees", "feature")
	testutil.FailErr(t, "mkdir worktree gitdir", os.MkdirAll(gitDir, 0o755))
	// Relative, the way git writes it.
	testutil.FailErr(t, "write commondir", os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644))

	worktree := canonicalTempDir(t)
	testutil.FailErr(t, "write .git file", os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644))

	repo, ok := gitrepo.Discover(worktree)
	if !ok {
		t.Fatal("Discover on a linked worktree found no repository")
	}
	if repo.Root != worktree {
		t.Fatalf("Root = %q, want %q", repo.Root, worktree)
	}
	if repo.GitDir != gitDir {
		t.Fatalf("GitDir = %q, want %q", repo.GitDir, gitDir)
	}
	if repo.CommonDir != commonDir {
		t.Fatalf("CommonDir = %q, want %q", repo.CommonDir, commonDir)
	}
}

func TestDiscoverSubmoduleGitDirIsItsOwnCommonDir(t *testing.T) {
	super := plainRepo(t)
	gitDir := filepath.Join(super, ".git", "modules", "vendor")
	testutil.FailErr(t, "mkdir module gitdir", os.MkdirAll(gitDir, 0o755))

	sub := filepath.Join(super, "vendor")
	testutil.FailErr(t, "mkdir submodule", os.MkdirAll(sub, 0o755))
	// Relative to the work tree holding the pointer, the way git writes it.
	testutil.FailErr(t, "write .git file", os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: ../.git/modules/vendor\n"), 0o644))

	repo, ok := gitrepo.Discover(sub)
	if !ok {
		t.Fatal("Discover on a submodule found no repository")
	}
	if repo.Root != sub {
		t.Fatalf("Root = %q, want the submodule work tree %q", repo.Root, sub)
	}
	if repo.GitDir != gitDir {
		t.Fatalf("GitDir = %q, want %q", repo.GitDir, gitDir)
	}
	if repo.CommonDir != gitDir {
		t.Fatalf("CommonDir = %q, want it to equal GitDir %q", repo.CommonDir, gitDir)
	}
}

func TestLocalConfigFilesCoverCommonAndWorktreeConfig(t *testing.T) {
	root := plainRepo(t)
	repo, ok := gitrepo.Discover(root)
	if !ok {
		t.Fatal("Discover found no repository")
	}
	got := repo.LocalConfigFiles()
	want := []string{
		filepath.Join(root, ".git", "config"),
		filepath.Join(root, ".git", "config.worktree"),
	}
	if len(got) != len(want) {
		t.Fatalf("LocalConfigFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("LocalConfigFiles = %v, want %v", got, want)
		}
	}
}

// An unresolved layout provides no configuration cache inputs.
func TestLocalConfigFilesEmptyWhenLayoutUnresolved(t *testing.T) {
	root := canonicalTempDir(t)
	testutil.FailErr(t, "write .git file", os.WriteFile(filepath.Join(root, ".git"), []byte("not a gitdir pointer\n"), 0o644))

	repo, ok := gitrepo.Discover(root)
	if !ok {
		t.Fatal("an unparsable .git pointer is still a repository boundary")
	}
	if repo.Root != root {
		t.Fatalf("Root = %q, want %q", repo.Root, root)
	}
	if repo.GitDir != "" || repo.CommonDir != "" {
		t.Fatalf("unresolved layout reported GitDir=%q CommonDir=%q", repo.GitDir, repo.CommonDir)
	}
	if files := repo.LocalConfigFiles(); files != nil {
		t.Fatalf("LocalConfigFiles = %v, want nil", files)
	}
}

func TestIsRootStopsAtTheNameNotTheTarget(t *testing.T) {
	dir := canonicalTempDir(t)
	if gitrepo.IsRoot(dir) {
		t.Fatal("a plain directory is not a repository root")
	}
	testutil.FailErr(t, "mkdir .git", os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	if !gitrepo.IsRoot(dir) {
		t.Fatal("a .git directory marks a repository root")
	}

	linked := canonicalTempDir(t)
	testutil.FailErr(t, "write .git file", os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: /nowhere\n"), 0o644))
	if !gitrepo.IsRoot(linked) {
		t.Fatal("a .git pointer file marks a repository root")
	}

	dangling := canonicalTempDir(t)
	testutil.FailErr(t, "symlink .git", os.Symlink(filepath.Join(dangling, "missing"), filepath.Join(dangling, ".git")))
	if !gitrepo.IsRoot(dangling) {
		t.Fatal("a dangling .git symlink is still a boundary somebody created")
	}
}

func TestCanonicalDirResolvesSymlinksAndKeepsAbsentPaths(t *testing.T) {
	real := canonicalTempDir(t)
	link := filepath.Join(canonicalTempDir(t), "link")
	testutil.FailErr(t, "symlink", os.Symlink(real, link))
	if got := gitrepo.CanonicalDir(link); got != real {
		t.Fatalf("CanonicalDir(link) = %q, want %q", got, real)
	}

	absent := filepath.Join(real, "not", "created", "yet")
	if got := gitrepo.CanonicalDir(absent); got != absent {
		t.Fatalf("CanonicalDir(absent) = %q, want %q", got, absent)
	}
	if got := gitrepo.CanonicalDir("   "); got != "" {
		t.Fatalf("CanonicalDir(blank) = %q, want \"\"", got)
	}
}
