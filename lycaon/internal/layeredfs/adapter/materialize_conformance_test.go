//go:build darwin || linux

package adapter

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

// conformanceSource builds a tree with every kind of metadata a prepared tree must keep, plus Git
// metadata it must leave out. It reports whether the volume took extended attributes and ACLs.
func conformanceSource(t *testing.T) (root string, xattrs, acls bool) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "source")
	old := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	for rel, body := range map[string]string{
		"plain.txt": "plain\n", "dir/sub/deep.txt": "deep\n", "dir/exec.sh": "#!/bin/sh\n",
		"repo/.git/HEAD": "ref: refs/heads/main\n", "repo/code.go": "package repo\n",
	} {
		path := filepath.Join(root, rel)
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "seed "+rel, os.WriteFile(path, []byte(body), 0o644))
	}
	testutil.FailErr(t, "exec mode", os.Chmod(filepath.Join(root, "dir/exec.sh"), 0o750))
	testutil.FailErr(t, "hard link", os.Link(filepath.Join(root, "plain.txt"), filepath.Join(root, "dir/hard.txt")))
	testutil.FailErr(t, "symlink", os.Symlink("../plain.txt", filepath.Join(root, "dir/link")))
	sparse, err := os.Create(filepath.Join(root, "sparse.bin"))
	testutil.FailErr(t, "sparse", err)
	_, err = sparse.WriteAt([]byte("end"), 32<<20)
	testutil.FailErr(t, "sparse write", errorsJoin(err, sparse.Close()))
	xattrs = unix.Lsetxattr(filepath.Join(root, "plain.txt"), "user.probe", []byte("value"), 0) == nil
	acls = setACL(filepath.Join(root, "dir/sub/deep.txt"))
	testutil.FailErr(t, "deep time", os.Chtimes(filepath.Join(root, "dir/sub/deep.txt"), old, old))
	testutil.FailErr(t, "read-only dir", os.Chmod(filepath.Join(root, "dir/sub"), 0o555))
	for _, dir := range []string{"dir/sub", "dir", "repo"} {
		testutil.FailErr(t, "dir time", os.Chtimes(filepath.Join(root, dir), old, old))
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "dir/sub"), 0o755) })
	return root, xattrs, acls
}

func errorsJoin(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// setACL gives path a non-trivial ACL where preparation carries one. Linux ACLs are extended
// attributes that preparation refuses unless they only restate the mode.
func setACL(path string) bool {
	return runtime.GOOS == "darwin" && exec.Command("chmod", "+a", "everyone deny delete", path).Run() == nil
}

func aclText(t *testing.T, path string) string {
	t.Helper()
	out, _ := exec.Command("ls", "-led", path).Output()
	lines := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	if len(lines) < 2 {
		return ""
	}
	return lines[1]
}

// A prepared tree matches its source entry by entry, by either preparation, and holds no Git metadata.
func TestPreparedTreeConformsToItsSource(t *testing.T) {
	for _, mode := range []struct {
		name     string
		dirClone DirectoryCloner
	}{
		{"host", nil},
		{"entry_by_entry", func(string, string) (bool, error) { return false, nil }},
	} {
		t.Run(mode.name, func(t *testing.T) {
			src, xattrs, acls := conformanceSource(t)
			tmp := t.TempDir()
			adp := NewEagerCopyAdapter()
			view, err := adp.Prepare(context.Background(), PrepareRequest{
				ViewID: "view-conform-" + mode.name, BackingDir: filepath.Join(tmp, "backing"), RuntimeDir: filepath.Join(tmp, "runtime"),
				Grants:         []RootGrant{{RootID: "primary", Label: "primary", SourcePath: src, IsPrimary: true}},
				CloneDirectory: mode.dirClone,
			})
			testutil.FailErr(t, "Prepare", err)
			t.Cleanup(func() { _, _ = adp.Release(context.Background(), view.ViewID) })
			seed, pristine := view.Roots[0].WorkspacePath, view.Roots[0].PristinePath
			if mode.dirClone == nil && runtime.GOOS == "darwin" && view.Materialize.ClonedDirectories == 0 {
				t.Fatalf("APFS cloned no directory: %+v", view.Materialize)
			}
			if view.Materialize.CopiedFiles == 0 && view.Materialize.ContentBytesRead != 0 {
				t.Fatalf("a cloned preparation read %d content bytes", view.Materialize.ContentBytesRead)
			}

			testutil.FailErr(t, "walk source", filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
				if err != nil || path == src {
					return err
				}
				rel, _ := filepath.Rel(src, path)
				if d.Name() == ".git" {
					for _, tree := range []string{seed, pristine} {
						if _, err := os.Lstat(filepath.Join(tree, rel)); !os.IsNotExist(err) {
							t.Fatalf("Git metadata %s reached a prepared tree: %v", rel, err)
						}
					}
					return filepath.SkipDir
				}
				want, _ := os.Lstat(path)
				got, err := os.Lstat(filepath.Join(seed, rel))
				if err != nil {
					t.Fatalf("%s missing: %v", rel, err)
				}
				if got.Mode() != want.Mode() {
					t.Fatalf("%s mode %v, want %v", rel, got.Mode(), want.Mode())
				}
				if want.Mode()&fs.ModeSymlink == 0 && !got.ModTime().Equal(want.ModTime()) {
					t.Fatalf("%s time %v, want %v", rel, got.ModTime(), want.ModTime())
				}
				if want.Mode()&fs.ModeSymlink != 0 {
					a, _ := os.Readlink(path)
					b, _ := os.Readlink(filepath.Join(seed, rel))
					if a != b {
						t.Fatalf("%s points to %q, want %q", rel, b, a)
					}
				}
				if want.Mode().IsRegular() && got.Sys().(*syscall.Stat_t).Blocks > want.Sys().(*syscall.Stat_t).Blocks {
					t.Fatalf("%s grew from %d to %d blocks", rel, want.Sys().(*syscall.Stat_t).Blocks, got.Sys().(*syscall.Stat_t).Blocks)
				}
				if acls && aclText(t, filepath.Join(seed, rel)) != aclText(t, path) {
					t.Fatalf("%s ACL %q, want %q", rel, aclText(t, filepath.Join(seed, rel)), aclText(t, path))
				}
				return nil
			}))
			if xattrs {
				buf := make([]byte, 64)
				n, err := unix.Lgetxattr(filepath.Join(seed, "plain.txt"), "user.probe", buf)
				if err != nil || string(buf[:n]) != "value" {
					t.Fatalf("extended attribute lost: %q %v", buf[:max(n, 0)], err)
				}
			}
			for _, tree := range []string{seed, pristine} {
				a, err := os.Lstat(filepath.Join(tree, "plain.txt"))
				testutil.FailErr(t, "stat plain", err)
				b, err := os.Lstat(filepath.Join(tree, "dir/hard.txt"))
				testutil.FailErr(t, "stat hard", err)
				if !os.SameFile(a, b) {
					t.Fatalf("hard-link identity lost in %s", tree)
				}
			}
		})
	}
}
