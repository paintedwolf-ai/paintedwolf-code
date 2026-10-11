package projectsource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRenameProjectSourceMoveCreatesParents(t *testing.T) {
	t.Parallel()
	p, root := writeTestProject(t, map[string]string{"a.txt": "hello\n"})
	testutil.FailErr(t, "chmod", os.Chmod(filepath.Join(root, "a.txt"), 0o640))

	got, err := renameProjectSource(p, SourceRenameRequest{From: "a.txt", To: "b/c.txt"})
	testutil.FailErr(t, "rename", err)
	if got.RootID != "r1" || got.Path != "b/c.txt" {
		t.Fatalf("result = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still present: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "b", "c.txt"))
	testutil.FailErr(t, "stat dest", err)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
	body, err := os.ReadFile(filepath.Join(root, "b", "c.txt"))
	testutil.FailErr(t, "read dest", err)
	if string(body) != "hello\n" {
		t.Fatalf("content = %q", body)
	}
}

func TestRenameProjectSourceCollisions(t *testing.T) {
	t.Parallel()
	rootA := t.TempDir()
	rootB := t.TempDir()
	testutil.FailErr(t, "write a", os.WriteFile(filepath.Join(rootA, "a.txt"), []byte("a"), 0o644))
	testutil.FailErr(t, "write b", os.WriteFile(filepath.Join(rootB, "only-on-b.txt"), []byte("b"), 0o644))
	testutil.FailErr(t, "write taken", os.WriteFile(filepath.Join(rootA, "taken.txt"), []byte("x"), 0o644))
	p := &Project{
		ID: "p1",
		Roots: []Root{
			{ID: "r1", Path: rootA, IsPrimary: true},
			{ID: "r2", Path: rootB},
		},
	}

	if _, err := renameProjectSource(p, SourceRenameRequest{
		RootID: "r1", From: "a.txt", To: "taken.txt",
	}); !errors.Is(err, ErrSourceExists) {
		t.Fatalf("onto existing err = %v, want ErrSourceExists", err)
	}
	if _, err := renameProjectSource(p, SourceRenameRequest{
		RootID: "r1", From: "only-on-b.txt", To: "from-other.txt",
	}); !errors.Is(err, ErrSourceCrossRoot) {
		t.Fatalf("cross-root err = %v, want ErrSourceCrossRoot", err)
	}
}

func TestCopyProjectSourceFileAndFolder(t *testing.T) {
	t.Parallel()
	p, root := writeTestProject(t, map[string]string{
		"src/a.txt": "one\n",
		"src/b.txt": "two\n",
	})
	testutil.FailErr(t, "chmod", os.Chmod(filepath.Join(root, "src", "a.txt"), 0o600))

	got, err := copyProjectSource(p, SourceCopyRequest{From: "src/a.txt", To: "dup/a.txt"})
	testutil.FailErr(t, "copy file", err)
	if got.Path != "dup/a.txt" {
		t.Fatalf("path = %q", got.Path)
	}
	info, err := os.Stat(filepath.Join(root, "dup", "a.txt"))
	testutil.FailErr(t, "stat copy", err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(root, "src", "a.txt")); err != nil {
		t.Fatalf("original missing: %v", err)
	}

	got, err = copyProjectSource(p, SourceCopyRequest{From: "src", To: "src-copy"})
	testutil.FailErr(t, "copy folder", err)
	if got.Path != "src-copy" {
		t.Fatalf("folder path = %q", got.Path)
	}
	for _, rel := range []string{"src-copy/a.txt", "src-copy/b.txt"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s missing: %v", rel, err)
		}
	}
	if _, err := copyProjectSource(p, SourceCopyRequest{From: "src/a.txt", To: "dup/a.txt"}); !errors.Is(err, ErrSourceExists) {
		t.Fatalf("copy onto existing err = %v", err)
	}
}

func TestLifecycleOpsJail(t *testing.T) {
	t.Parallel()
	p, root := writeTestProject(t, map[string]string{"ok.txt": "x\n"})
	testutil.FailErr(t, "mkdir git", os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	testutil.FailErr(t, "write git", os.WriteFile(filepath.Join(root, ".git", "config"), []byte("x"), 0o644))
	testutil.FailErr(t, "mkdir lycaon", os.MkdirAll(filepath.Join(root, settingsoverlay.DirName()), 0o755))
	testutil.FailErr(t, "write lycaon", os.WriteFile(filepath.Join(root, settingsoverlay.DirName(), "x"), []byte("x"), 0o644))

	denied := []string{"../escape", ".git/config", settingsoverlay.Rel("x")}
	invalid := []string{".", "./"}

	for _, path := range denied {
		want := ErrSourcePathProtected
		if path == "../escape" {
			want = ErrSourcePathDenied
		}
		if _, err := renameProjectSource(p, SourceRenameRequest{From: path, To: "out.txt"}); !errors.Is(err, want) {
			t.Fatalf("rename from %q err = %v, want denied", path, err)
		}
		if _, err := renameProjectSource(p, SourceRenameRequest{From: "ok.txt", To: path}); !errors.Is(err, want) && !errors.Is(err, ErrSourceExists) {
			// .git/config and .paintedwolf/x exist → Exists; ../escape → Denied.
			if path == "../escape" && !errors.Is(err, want) {
				t.Fatalf("rename to %q err = %v, want denied", path, err)
			}
			if path != "../escape" && !errors.Is(err, ErrSourceExists) && !errors.Is(err, want) {
				t.Fatalf("rename to %q err = %v", path, err)
			}
		}
		if _, err := copyProjectSource(p, SourceCopyRequest{From: path, To: "out2.txt"}); !errors.Is(err, want) {
			t.Fatalf("copy from %q err = %v, want denied", path, err)
		}
		if err := deleteProjectSource(p, SourceDeleteRequest{Path: path}); !errors.Is(err, want) {
			t.Fatalf("delete %q err = %v, want denied", path, err)
		}
	}
	for _, path := range invalid {
		if _, err := renameProjectSource(p, SourceRenameRequest{From: path, To: "out.txt"}); !errors.Is(err, ErrSourcePathInvalid) {
			t.Fatalf("rename from %q err = %v, want invalid", path, err)
		}
		if _, err := renameProjectSource(p, SourceRenameRequest{From: "ok.txt", To: path}); !errors.Is(err, ErrSourcePathInvalid) {
			t.Fatalf("rename to %q err = %v, want invalid", path, err)
		}
		if err := deleteProjectSource(p, SourceDeleteRequest{Path: path}); !errors.Is(err, ErrSourcePathInvalid) {
			t.Fatalf("delete %q err = %v, want invalid", path, err)
		}
	}
	// Reserved dest that does not exist yet is still denied.
	if _, err := renameProjectSource(p, SourceRenameRequest{From: "ok.txt", To: ".git/new"}); !errors.Is(err, ErrSourcePathProtected) {
		t.Fatalf("rename into .git err = %v, want denied", err)
	}
}
