package gitrepo

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		testutil.FailErr(t, "mkdir "+path, os.MkdirAll(path, 0o755))
	}
}

func writeText(t *testing.T, path, text string) {
	t.Helper()
	testutil.FailErr(t, "write "+path, os.WriteFile(path, []byte(text), 0o644))
}

func bare(t *testing.T, dir string) {
	t.Helper()
	mkdirs(t, filepath.Join(dir, "objects", "info"), filepath.Join(dir, "refs"))
	writeText(t, filepath.Join(dir, "HEAD"), "ref: refs/heads/main\n")
}

// A linked worktree opens its own git directory and the main repository's.
func TestRepositoryDirsFollowALinkedWorktree(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "root", err)
	main := filepath.Join(root, "main", ".git")
	linked := filepath.Join(main, "worktrees", "feature")
	mkdirs(t, filepath.Join(main, "objects"), linked, filepath.Join(root, "feature"))
	writeText(t, filepath.Join(linked, "commondir"), "../..\n")
	writeText(t, filepath.Join(root, "feature", ".git"), "gitdir: "+linked+"\n")

	own, shared, ok := RepositoryDirs(filepath.Join(root, "feature"))
	if !ok || len(shared) != 0 {
		t.Fatalf("ok %v, shared %v", ok, shared)
	}
	if want := []string{linked, main, filepath.Join(main, "objects")}; !slices.Equal(own, want) {
		t.Fatalf("own %v, want %v", own, want)
	}
}

// A bare repository's alternates, relative, quoted and chained, are the
// object stores it reads; a symlinked store resolves to its target.
func TestRepositoryDirsFollowAlternatesAndSymlinks(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "root", err)
	repo, first, second := filepath.Join(root, "repo.git"), filepath.Join(root, "first"), filepath.Join(root, "with space")
	bare(t, repo)
	mkdirs(t, filepath.Join(first, "objects", "info"), filepath.Join(second, "objects"), filepath.Join(root, "real-objects", "info"))
	writeText(t, filepath.Join(repo, "objects", "info", "alternates"), "# shared\n../../first/objects\n")
	writeText(t, filepath.Join(first, "objects", "info", "alternates"), `"`+filepath.Join(second, "objects")+`"`+"\n")

	own, shared, ok := RepositoryDirs(repo)
	if !ok || !slices.Equal(own, []string{repo, filepath.Join(repo, "objects")}) {
		t.Fatalf("ok %v, own %v", ok, own)
	}
	if want := []string{filepath.Join(first, "objects"), filepath.Join(second, "objects")}; !slices.Equal(shared, want) {
		t.Fatalf("shared %v, want %v", shared, want)
	}

	linked := filepath.Join(root, "linked.git")
	mkdirs(t, filepath.Join(linked, "refs"))
	writeText(t, filepath.Join(linked, "HEAD"), "ref: refs/heads/main\n")
	testutil.FailErr(t, "symlink objects", os.Symlink(filepath.Join(root, "real-objects"), filepath.Join(linked, "objects")))
	own, _, ok = RepositoryDirs(linked)
	if !ok || !slices.Contains(own, filepath.Join(root, "real-objects")) {
		t.Fatalf("ok %v, own %v; want the symlink target", ok, own)
	}

	if _, _, ok := RepositoryDirs(filepath.Join(root, "first")); ok {
		t.Fatal("a directory that is not a repository reported one")
	}
}
